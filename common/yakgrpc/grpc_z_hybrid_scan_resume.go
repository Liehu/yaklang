package yakgrpc

import (
	"container/list"
	"context"
	"encoding/json"
	"github.com/yaklang/yaklang/common/consts"
	"github.com/yaklang/yaklang/common/fp"
	"github.com/yaklang/yaklang/common/log"
	"github.com/yaklang/yaklang/common/utils"
	"github.com/yaklang/yaklang/common/utils/lowhttp"
	"github.com/yaklang/yaklang/common/yak/yaklib"
	"github.com/yaklang/yaklang/common/yakgrpc/yakit"
	"github.com/yaklang/yaklang/common/yakgrpc/ypb"
	"math/rand"
	"sync"
	"time"
)

func (s *Server) hybridScanResume(manager *HybridScanTaskManager, stream HybridScanRequestStream) error {
	task, err := yakit.GetHybridScanByTaskId(s.GetProjectDatabase(), manager.TaskId())
	if err != nil {
		return utils.Wrapf(err, "Resume HybridScanByID: %v", manager.TaskId())
	}
	var scanConfig ypb.HybridScanRequest
	err = json.Unmarshal(task.ScanConfig, &scanConfig)
	if err != nil {
		return utils.Wrapf(err, "Resume HybridScanByID: %v", manager.TaskId())
	}

	quickSave := func() {
		if consts.GetGormProjectDatabase().Save(task).Error != nil {
			log.Error(err)
		}
	}
	defer func() {
		if err := recover(); err != nil {
			task.Reason = utils.Wrapf(utils.Error(err), "PANIC from resume").Error()
			task.Status = yakit.HYBRIDSCAN_ERROR
			quickSave()
			return
		}

		if task.Status == yakit.HYBRIDSCAN_PAUSED {
			quickSave()
			return
		}
		task.Status = yakit.HYBRIDSCAN_DONE
		quickSave()
	}()

	var hashMap = make(map[int64]struct{})
	var minIndex int64 = -1
	var maxIndex int64 = 0
	// string to int
	for _, val := range utils.ParseStringToPorts(task.SurvivalTaskIndexes) {
		val := int64(val)
		hashMap[val] = struct{}{}
		if minIndex == -1 {
			minIndex = val
		} else {
			if val < minIndex {
				minIndex = val
			}
		}
		if val > maxIndex {
			maxIndex = val
		}
	}

	var targets []*HybridScanTarget
	var pluginName []string
	err = json.Unmarshal([]byte(task.Targets), &targets)
	if err != nil {
		return utils.Wrapf(err, "Unmarshal HybridScan Targets: %v", task.Targets)
	}
	err = json.Unmarshal([]byte(task.Plugins), &pluginName)
	if err != nil {
		return utils.Wrapf(err, "Unmarshal HybridScan Plugins: %v", task.Plugins)
	}

	statusManager := newHybridScanStatusManager(task.TaskId, len(targets), len(pluginName))
	statusManager.SetCurrentTaskIndex(minIndex)

	pluginCacheList := list.New()
	feedbackStatus := func() {
		statusManager.Feedback(stream)
	}

	swg := utils.NewSizedWaitGroup(int(scanConfig.Concurrent))                                                                     // 设置并发数
	manager.ctx, manager.cancel = context.WithTimeout(manager.Context(), time.Duration(scanConfig.TotalTimeoutSecond)*time.Second) // 设置总超时
	// init some config
	var riskCount, _ = yakit.CountRiskByRuntimeId(s.GetProfileDatabase(), task.TaskId)
	var resumeFilterManager = NewFilterManager(12, 1<<15, 30)
	var hasUnavailableTarget = false

	matcher, err := fp.NewDefaultFingerprintMatcher(fp.NewConfig(fp.WithDatabaseCache(true), fp.WithCache(true)))
	if err != nil {
		return utils.Wrap(err, "init fingerprint matcher failed")
	}

	go func() { // 监听控制信号
		for {
			rsp, err := stream.Recv()
			if err != nil {
				task.Reason = err.Error()
				return
			}
			if rsp.HybridScanMode == "pause" {
				manager.isPaused.Set()
				manager.cancel()
				statusManager.GetStatus(task)
				task.Status = yakit.HYBRIDSCAN_PAUSED
				quickSave()
			}
			if rsp.HybridScanMode == "stop" {
				manager.isPaused.Set()
				manager.cancel()
				statusManager.GetStatus(task)
				quickSave()
			}
		}
	}()

	// dispatch
	for _, __currentTarget := range targets {
		if manager.IsPaused() { // 如果暂停立刻停止
			break
		}
		statusManager.DoActiveTarget()
		feedbackStatus()
		targetWg := new(sync.WaitGroup)

		resp, err := lowhttp.HTTPWithoutRedirect(lowhttp.WithPacketBytes(__currentTarget.Request), lowhttp.WithHttps(__currentTarget.IsHttps), lowhttp.WithRuntimeId(task.TaskId))
		if err != nil {
			log.Errorf("request target failed: %s", err)
			hasUnavailableTarget = true
			continue
		}
		__currentTarget.Response = resp.RawPacket

		// fingerprint match just once
		var portScanCond = &sync.Cond{L: &sync.Mutex{}}
		var fingerprintMatchOK = false
		host, port, _ := utils.ParseStringToHostPort(__currentTarget.Url)
		go func() {
			_, err = matcher.Match(host, port)
			if err != nil {
				log.Errorf("match fingerprint failed: %s", err)
			}
			portScanCond.L.Lock()
			defer portScanCond.L.Unlock()
			portScanCond.Broadcast()
			fingerprintMatchOK = true
		}()

		// load plugins
		pluginChan, err := s.PluginGenerator(pluginCacheList, manager.Context(), &ypb.HybridScanPluginConfig{PluginNames: pluginName})
		if err != nil {
			return utils.Wrapf(err, "PluginGenerator")
		}

		for __pluginInstance := range pluginChan {
			targetRequestInstance := __currentTarget
			pluginInstance := __pluginInstance

			if swgErr := swg.AddWithContext(manager.Context()); swgErr != nil {
				continue
			}
			targetWg.Add(1)

			manager.Checkpoint(func() {
				task.SurvivalTaskIndexes = utils.ConcatPorts(statusManager.GetCurrentActiveTaskIndexes())
				feedbackStatus()
				task.Status = yakit.HYBRIDSCAN_PAUSED
				quickSave()
			})
			if manager.IsPaused() { // 如果暂停立刻停止
				break
			}

			for !fingerprintMatchOK { // wait for fingerprint match
				portScanCond.L.Lock()
				portScanCond.Wait()
				portScanCond.L.Unlock()
			}

			taskIndex := statusManager.DoActiveTask()

			feedbackStatus()

			go func() {
				defer swg.Done()

				defer targetWg.Done()
				defer func() {
					if !manager.IsPaused() { // 暂停之后不再更新进度
						statusManager.DoneTask(taskIndex)
					}
					feedbackStatus()
					statusManager.RemoveActiveTask(taskIndex, targetRequestInstance, pluginInstance.ScriptName, stream)
				}()

				// shrink context
				select {
				case <-manager.Context().Done():
					log.Infof("skip task %d via canceled", taskIndex)
					return
				default:
				}

				statusManager.PushActiveTask(taskIndex, targetRequestInstance, pluginInstance.ScriptName, stream)

				// 过滤执行过的任务
				// 小于最小索引的任务，直接跳过
				// 大于最大索引的任务，直接执行
				// 在最小索引和最大索引之间的任务，如果没有执行过，执行
				if taskIndex < minIndex {
					return
				} else if taskIndex >= minIndex && taskIndex <= maxIndex {
					_, ok := hashMap[taskIndex]
					if !ok {
						return
					}
				}

				feedbackClient := yaklib.NewVirtualYakitClientWithRiskCount(func(result *ypb.ExecResult) error {
					// shrink context
					select {
					case <-manager.Context().Done():
						return nil
					default:
					}

					result.RuntimeID = task.TaskId
					currentStatus := statusManager.GetStatus()
					currentStatus.CurrentPluginName = pluginInstance.ScriptName
					currentStatus.ExecResult = result
					return stream.Send(currentStatus)
				}, &riskCount)
				callerFilter := resumeFilterManager.DequeueFilter()
				defer resumeFilterManager.EnqueueFilter(callerFilter)
				err := s.ScanTargetWithPlugin(task.TaskId, manager.Context(), targetRequestInstance, pluginInstance, scanConfig.Proxy, feedbackClient, callerFilter)
				if err != nil {
					log.Warnf("scan target failed: %s", err)
				}
				time.Sleep(time.Duration(300+rand.Int63n(700)) * time.Millisecond)
			}()

		}
		// shrink context
		select {
		case <-manager.Context().Done():
			return utils.Error("task manager canceled")
		default:
		}

		go func() {
			// shrink context
			select {
			case <-manager.Context().Done():
				return
			default:
			}

			targetWg.Wait()
			statusManager.DoneTarget()
			feedbackStatus()
		}()
	}
	swg.Wait()
	feedbackStatus()
	statusManager.GetStatus(task)
	if !manager.IsPaused() {
		task.Status = yakit.HYBRIDSCAN_DONE
	}
	quickSave()
	if hasUnavailableTarget {
		return utils.Errorf("Has unreachable target")
	}
	return nil
}
