package webfingerprint

import (
	"fmt"
	"github.com/jinzhu/copier"
	"github.com/pkg/errors"
	log "github.com/yaklang/yaklang/common/log"
	utils2 "github.com/yaklang/yaklang/common/utils"
	"github.com/yaklang/yaklang/common/utils/lowhttp"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

type HTTPResponseInfo struct {
	StatusCode int
	Status     string
	Header     *http.Header
	Body       []byte
	URL        *url.URL
	RequestRaw []byte
	IsHttps    bool
}

func (h *HTTPResponseInfo) Bytes() []byte {
	var lines []string
	lines = append(lines, fmt.Sprintf("HTTP/1.1 %v %v", h.StatusCode, h.Status))
	if h.Header != nil {
		for k, v := range *h.Header {
			for _, value := range v {
				lines = append(lines, fmt.Sprintf("%v: %v", k, value))
			}
		}
	}
	lines = append(lines, "")
	lines = append(lines, "")
	lines = append(lines, string(h.Body))
	return []byte(strings.Join(lines, "\r\n"))
}

func (h *HTTPResponseInfo) ResponseHeaderBytes() []byte {
	header, _ := lowhttp.SplitHTTPHeadersAndBodyFromPacket(h.Bytes())
	return []byte(header)
}

func ExtractHTTPResponseInfoFromHTTPResponse(res *http.Response) *HTTPResponseInfo {
	limit := 2048 * 10
	return ExtractHTTPResponseInfoFromHTTPResponseWithBodySize(res, limit)
}

func ExtractHTTPResponseInfoFromHTTPResponseWithBodySize(res *http.Response, size int) *HTTPResponseInfo {
	//body, _ := utils.ReadWithLen(res.Body, 1024*1024*10)
	body, _ := utils2.ReadWithLen(res.Body, size)

	return &HTTPResponseInfo{
		StatusCode: res.StatusCode,
		Status:     res.Status,
		Header:     &res.Header,
		Body:       body,
		URL:        res.Request.URL,
	}
}

type Matcher struct {
	config *Config
}

func NewWebFingerprintMatcherWithConfig(config *Config) *Matcher {
	return &Matcher{config: config}
}

func NewWebFingerprintMatcher(rules []*WebRule, active bool, allRules bool) (*Matcher, error) {
	return NewWebFingerprintMatcherWithConfig(NewWebFingerprintConfig(
		WithWebFingerprintRules(rules),
		WithActiveMode(active),
		WithForceAllRuleMatching(allRules),
	)), nil
}

func foreachHTTPHeaders(h *http.Header, f func(string, string) bool) {
	for name, values := range *h {
		for _, value := range values {
			if !f(name, value) {
				return
			}
		}
	}
}

func (f *Matcher) match(r *HTTPResponseInfo, options ...ConfigOption) ([]*CPE, error) {
	var config = NewWebFingerprintConfig()
	err := copier.Copy(config, f.config)
	if err != nil {
		return nil, errors.Errorf("create new temporary config failed: %s", err)
	}
	for _, option := range options {
		option(config)
	}

	results := f.matchWithConfig(r, config)
	if len(results) > 0 {
		return results, nil
	}

	return nil, errors.Errorf("failed to recognize web fingerprint: %s", "no rules matched")
}

var faviconCache sync.Map
var currentTarget = ""
var previousTarget = ""

func (f *Matcher) matchByRule(r *HTTPResponseInfo, ruleToUse *WebRule, config *Config) []*CPE {
	defer func() {
		if err := recover(); err != nil {
			log.Errorf("matchByRule failed: %s", err)
		}
	}()

	var cpes []*CPE
	if ruleToUse == nil || f == nil {
		return cpes
	}

	// filter active detect
	path := "/"
	if r.URL == nil {
		r.URL, _ = lowhttp.ExtractURLFromHTTPRequestRaw(r.RequestRaw, r.IsHttps)
	}
	if r.URL != nil {
		path = r.URL.Path
	} else {
		if r.IsHttps {
			r.URL, _ = url.Parse("https://127.0.0.1/")
		} else {
			r.URL, _ = url.Parse("http://127.0.0.1/")
		}
	}

	// Check if the target has changed
	if r.URL.Host != currentTarget {
		// Delete the cache of the previous target
		if previousTarget != "" {
			faviconCache.Delete(previousTarget)
		}
		// Update the current and previous targets
		previousTarget = currentTarget
		currentTarget = r.URL.Host
	}

	if !strings.HasPrefix(ruleToUse.Path, "/") {
		ruleToUse.Path = "/" + ruleToUse.Path
	}
	if config.ActiveMode && len(ruleToUse.Path) > 1 && !strings.HasSuffix(ruleToUse.Path, path) {
		value, ok := faviconCache.Load(r.URL.Host)
		if !ok {
			log.Debugf("sending active web-fingerprint request to: %s origin: %v", ruleToUse.Path, path)
			newUrl := lowhttp.MergeUrlFromHTTPRequest(r.RequestRaw, ruleToUse.Path, r.IsHttps)
			request := lowhttp.UrlToGetRequestPacket(newUrl, r.RequestRaw, r.IsHttps, lowhttp.ExtractCookieJarFromHTTPResponse(
				append(r.ResponseHeaderBytes(), r.Body...))...)
			host, port, _ := utils2.ParseStringToHostPort(r.URL.String())
			isOpen, infos, err := FetchBannerFromHostPortEx(
				utils2.TimeoutContext(config.ProbeTimeout), request, host, port, int64(config.FingerprintDataSize),
				config.Proxies...)
			if err != nil {
				log.Errorf("fetch banner for %v failed: %s", newUrl, err)
				return nil
			}
			_ = isOpen
			var results []*CPE
			for _, i := range infos {
				if i == nil {
					continue
				}
				byteFavicon, ok := value.([]byte)
				if !ok {
					byteFavicon = i.Body
					faviconCache.Store(r.URL.Host, byteFavicon)
				}
				// Use the cached favicon
				i.Body = byteFavicon
				results = append(results, f.matchByRule(i, ruleToUse, config)...)
			}
			return results
		} else {
			favicon, ok := value.([]byte)
			if !ok {
				log.Errorf("Expected []byte but got %T", value)
				return nil
			}
			r.Body = favicon
		}
	}

	for _, m := range ruleToUse.Methods {
		if m.Condition == "and" {
			allMatched := true
			var tempCpes []*CPE
			for _, k := range m.Keywords {
				cpe, err := k.Match(string(r.Bytes()))
				if err != nil {
					allMatched = false
					break
				}
				tempCpes = append(tempCpes, cpe)
			}
			if allMatched {
				cpes = append(cpes, tempCpes...)
			}
		} else {
			for _, k := range m.Keywords {
				cpe, err := k.Match(string(r.Bytes()))
				if err != nil {
					//log.Debugf("keyword match[%s] failed: %s", k.regexp.String(), err)
					continue
				}
				cpes = append(cpes, cpe)
			}
		}

		// 匹配 HTTP Headers
		for _, h := range m.HTTPHeaders {
			foreachHTTPHeaders(r.Header, func(s string, s2 string) bool {
				cpe, err := h.Match(s, s2)
				if err != nil {
					//log.Debugf("compare header[%s] failed: %s", h, err)
					return true
				}

				cpes = append(cpes, cpe)
				return true
			})
		}

		// 匹配页面内容 MD5
		for _, m := range m.MD5s {
			cpe, err := m.Match(r.Body)
			if err != nil {
				//log.Debugf("match body md5[%s] failed: %s", m.MD5, err)
				continue
			}

			cpes = append(cpes, cpe)
		}
	}
	return cpes
}

func (f *Matcher) matchWithConfig(rsp *HTTPResponseInfo, config *Config) []*CPE {
	var cpes []*CPE
	if rsp == nil {
		return cpes
	}
	for _, rule := range config.Rules {
		rule := rule
	MatchNext:
		results := f.matchByRule(rsp, rule, config)
		if len(results) > 0 {
			cpes = append(cpes, results...)
			if rule.NextStep != nil {
				rule = rule.NextStep
				goto MatchNext
			} else {
				continue
			}
		}

	}

	return cpes
}

func (f *Matcher) Match(rsp *HTTPResponseInfo) ([]*CPE, error) {
	return f.match(rsp)
}

func (f *Matcher) MatchWithRules(rsp *HTTPResponseInfo, rules []*WebRule) ([]*CPE, error) {
	return f.match(rsp, WithWebFingerprintRules(rules))
}

func (f *Matcher) MatchWithOptions(rsp *HTTPResponseInfo, options ...ConfigOption) ([]*CPE, error) {
	return f.match(rsp, options...)
}
