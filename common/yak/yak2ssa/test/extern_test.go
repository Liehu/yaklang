package test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/yaklang/yaklang/common/yak/ssaapi"
)

func TestExternLibInClosure(t *testing.T) {
	test := assert.New(t)
	prog, err := ssaapi.Parse(`
	a = () => {
		lib.method()
	}
	`,
		ssaapi.WithExternLib("lib", map[string]any{
			"method": func() {},
		}),
	)
	test.Nil(err)
	libVariables := prog.Ref("lib").ShowWithSource()
	// TODO: handler this
	// test.Equal(1, len(libVariables))
	test.NotEqual(0, len(libVariables))
	libVariable := libVariables[0]

	test.False(libVariable.IsParameter())
	test.True(libVariable.IsExternLib())
}

func TestExternValue(t *testing.T) {

	check := func(t *testing.T, tc TestCase, checkFunc func(*testing.T, *ssaapi.Program)) {
		tc.ExternValue = map[string]any{
			"println": func(v ...any) {},
		}
		tc.Check = func(t *testing.T, p *ssaapi.Program, s []string) {
			checkFunc(t, p)
		}
		CheckTestCase(t, tc)
	}
	checkIsFunction := func(t *testing.T, tc TestCase) {
		check(t, tc,
			func(t *testing.T, prog *ssaapi.Program) {
				ps := prog.Ref("target").ShowWithSource()
				if len(ps) == 0 {
					t.Fatalf("target should has value")
				}
				if len(
					ps.Filter(func(v *ssaapi.Value) bool {
						return !v.IsFunction()
					}),
				) != 0 {
					t.Fatalf("target should all is function")
				}
			},
		)
	}

	t.Run("use normal", func(t *testing.T) {
		checkIsFunction(t, TestCase{
			code: `
			target = println
			`,
		})
	})

	t.Run("use in loop", func(t *testing.T) {
		checkIsFunction(t, TestCase{
			code: `
			for i=0; i<10;i++{
				target = println
			}
			`,
		})
	})

	t.Run("use in closure", func(t *testing.T) {
		checkIsFunction(t, TestCase{
			code: `
			f = () => {
				target = println
			}
			`,
		})
	})

	t.Run("use in closure can capture", func(t *testing.T) {
		checkIsFunction(t, TestCase{
			code: `
			target = println
			f = () => {
				target = println
			}
			`,
		})
	})

	checkIsCover := func(t *testing.T, tc TestCase) {
		check(t, tc,
			func(t *testing.T, p *ssaapi.Program) {
				ps := p.Ref("target").ShowWithSource()
				if len(ps) == 0 {
					t.Fatalf("target should has value")
				}
				if len(
					ps.Filter(func(v *ssaapi.Value) bool {
						return v.String() != "1"
					}),
				) != 0 {
					t.Fatalf("println should all is 1")
				}
			},
		)
	}

	t.Run("cover normal", func(t *testing.T) {
		checkIsCover(t, TestCase{
			code: `
			println = 1
			target = println
			`,
		})
	})

	t.Run("cover check in syntax block", func(t *testing.T) {
		checkIsCover(t, TestCase{
			code: `
			println = 1
			{
				target = println
			}
			`,
		})
	})

	t.Run("cover assign in syntax block", func(t *testing.T) {
		checkIsFunction(t, TestCase{
			code: `
			{
				println = 1
			}
			target = println // this is function
			`,
		})
	})

	t.Run("cover in loop", func(t *testing.T) {
		checkIsCover(t, TestCase{
			code: `
			println = 1
			for i in 10{
				target = println
			}
			`,
		})
	})
}

func TestExternLib(t *testing.T) {

	check := func(t *testing.T, tc TestCase, checkFunc func(*testing.T, *ssaapi.Program)) {
		// tc.ExternValue = map[string]any{
		// 	"println": func(v ...any) {},
		// }
		tc.ExternLib = map[string]map[string]any{
			"lib": map[string]any{
				"method": func() {},
			},
		}
		tc.Check = func(t *testing.T, p *ssaapi.Program, s []string) {
			checkFunc(t, p)
		}
		CheckTestCase(t, tc)
	}
	checkIsExtern := func(t *testing.T, tc TestCase) {
		check(t, tc,
			func(t *testing.T, prog *ssaapi.Program) {
				if len(
					prog.Ref("lib").Filter(func(v *ssaapi.Value) bool {
						return !v.IsExternLib()
					}),
				) != 0 {
					t.Fatalf("lib shoud all is externLib")
				}

				ps := prog.Ref("target").ShowWithSource()
				if len(
					ps.Filter(func(v *ssaapi.Value) bool {
						return !v.IsFunction()
					}),
				) != 0 {
					t.Fatalf("println should all is function")
				}
			},
		)
	}

	t.Run("use normal", func(t *testing.T) {
		checkIsExtern(t, TestCase{
			code: `
			target = lib.method
			`,
		})
	})

	t.Run("use in loop", func(t *testing.T) {
		checkIsExtern(t, TestCase{
			code: `
			for i in 10 {
				target = lib.method
			}
			`,
		})
	})

	t.Run("use in closure", func(t *testing.T) {
		checkIsExtern(t, TestCase{
			code: `
			f = () => {
				target = lib.method
			}
			`,
		})
	})

	t.Run("use in closure, can capture method", func(t *testing.T) {
		checkIsExtern(t, TestCase{
			code: `
				target = lib.method
				f = () => {
					target = lib.method
				}
				`,
		})
	})

	t.Run("use in closure, can capture lib", func(t *testing.T) {
		checkIsExtern(t, TestCase{
			code: `
				b = lib
				f = () => {
					target = lib.method
				}
				`,
		})
	})

	checkIsCover := func(t *testing.T, tc TestCase) {
		check(t, tc,
			func(t *testing.T, p *ssaapi.Program) {
				ps := p.Ref("target").ShowWithSource()
				if len(ps) == 0 {
					t.Fatalf("target should has value")
				}
				if len(
					ps.Filter(func(v *ssaapi.Value) bool {
						return v.String() != "1"
					}),
				) != 0 {
					t.Fatalf("target should all is 1")
				}
			},
		)
	}

	t.Run("cover normal  ", func(t *testing.T) {
		checkIsCover(t, TestCase{
			code: `
			lib.method = 1
			target = lib.method
			`,
		})
	})

	t.Run("cover assign in syntax block", func(t *testing.T) {
		checkIsExtern(t, TestCase{
			code: `
			{
				lib.method = 1
			}
			target = lib.method
			`,
		})
	})

	t.Run("cover check in syntax block", func(t *testing.T) {
		checkIsCover(t, TestCase{
			code: `
			lib.method = 1
			{
				target = lib.method
			}
			`,
		})
	})

	t.Run("cover check in loop", func(t *testing.T) {
		checkIsCover(t, TestCase{
			code: `
			lib.method = 1
			for i in 10{
				target = lib.method
			}
			`,
		})
	})
}

func TestExternRef(t *testing.T) {
	check := func(t *testing.T, tc TestCase) {
		tc.ExternValue = map[string]any{
			"println": func(v ...any) {},
		}
		tc.ExternLib = map[string]map[string]any{
			"lib": map[string]any{
				"method": func() {},
			},
		}
		CheckTestCase(t, tc)
	}

	t.Run("extern value", func(t *testing.T) {
		check(t, TestCase{
			code: `
			println("a")
			println("a")
			println("a")
			println("a")
			`,
			Check: func(t *testing.T, p *ssaapi.Program, want []string) {
				test := assert.New(t)
				printlns := p.Ref("println")
				test.Len(printlns, 1)

				println := printlns[0]
				test.True(println.IsFunction())

				printlnCaller := println.GetUsers()
				test.Len(printlnCaller, 4)
			},
		})
	})

	t.Run("extern lib", func(t *testing.T) {
		check(t, TestCase{
			code: `
			lib.method()
			lib.method()
			lib.method()
			lib.method()
			lib.method()
			lib.method()
			lib.method()
			`,
			Check: func(t *testing.T, p *ssaapi.Program, w []string) {
				test := assert.New(t)

				libs := p.Ref("lib")
				test.Len(libs, 1)

				lib := libs[0]
				test.True(lib.IsExternLib())

				methods := lib.GetOperands()
				test.Len(methods, 1)

				method := methods[0]
				test.True(method.IsFunction())

				methodCaller := method.GetUsers()
				test.Len(methodCaller, 7)
			},
		})
	})

}
