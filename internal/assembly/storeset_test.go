package assembly

// 提案 2026-11-05 C7 验收锚：newStoreSet 单源完整性。五份手写 repo 清单
// （NewBinder/NewAuthnRepos/NewSeedRepos/anchorRepos/NewAPIDeps）收敛为
// 单源构造 + 子集投影后，"单源漏填一个字段"不再有其余清单的冗余兜底——
// 本测试以反射遍历 newStoreSet 产物的全部字段断言非 nil（接口/指针/函数），
// 漏字段即红。repo 构造不拨库（纯字面量持 clock+runner），零值时钟与假
// runner 即可 hermetic 构造——假 runner 的执行方法全部 panic，越界即测试
// 红（构造期触碰执行面 = 前提失效）。

import (
	"context"
	"database/sql"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// storeSetNilExemptions 是恒零字段豁免清单（字段名 → 理由）。当前为空：
// transact.Stores 全部字段都被 newStoreSet 填充；未来若出现恒零的可选槽，
// 在此登记理由后才许缺省——不在清单内的 nil 一律红。
var storeSetNilExemptions = map[string]string{}

// storeSetZeroClock 是反射夹具的零值时钟（repo 构造只存引用不读时间）。
type storeSetZeroClock struct{}

func (storeSetZeroClock) Now() time.Time { return time.Time{} }

// storeSetPanicRunner 是反射夹具的假执行面（构造期不得触碰执行方法）。
type storeSetPanicRunner struct{}

func (storeSetPanicRunner) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	panic("repo construction must not execute statements")
}
func (storeSetPanicRunner) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	panic("repo construction must not execute statements")
}
func (storeSetPanicRunner) QueryRowContext(context.Context, string, ...any) *sql.Row {
	panic("repo construction must not execute statements")
}

// TestNewStoreSetFillsEveryField：单源漏字段回归网——Stores 新增聚合 repo
// 字段而 newStoreSet 未填（或既有字段被误删）时，对应字段零值 nil 即红，
// 报错点名缺失字段（填补单源，或登记豁免清单带理由）。
func TestNewStoreSetFillsEveryField(t *testing.T) {
	s := newStoreSet(storeSetZeroClock{}, storeSetPanicRunner{})
	typ := reflect.TypeOf(s)
	val := reflect.ValueOf(s)
	for i := range typ.NumField() {
		name := typ.Field(i).Name
		if _, exempt := storeSetNilExemptions[name]; exempt {
			continue
		}
		switch val.Field(i).Kind() {
		case reflect.Interface, reflect.Pointer, reflect.Func,
			reflect.Map, reflect.Slice, reflect.Chan:
			assert.NotNil(t, val.Field(i).Interface(),
				"transact.Stores.%s is nil — the newStoreSet single source dropped this field (fill it, or register a storeSetNilExemptions entry with a reason)", name)
		default:
			t.Errorf("transact.Stores.%s is a value field (%s) — the reflection completeness test only nil-checks reference kinds; extend the test", name, val.Field(i).Kind())
		}
	}
}
