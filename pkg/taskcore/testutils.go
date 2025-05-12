package taskcore

import (
	"fmt"
	reflect "reflect"

	"github.com/cloudcarver/anchor/pkg/apigen"
	"github.com/jackc/pgx/v5"
	gomock "go.uber.org/mock/gomock"
)

type OverrideMatcher struct {
	f TaskOverride
}

func Eq(f TaskOverride) *OverrideMatcher {
	return &OverrideMatcher{f: f}
}

func (m *OverrideMatcher) Matches(x any) bool {
	fx := x.(TaskOverride)

	a := apigen.Task{}
	b := apigen.Task{}

	m.f(&a)
	fx(&b)

	return reflect.DeepEqual(a, b)
}

func (m *OverrideMatcher) String() string {
	return fmt.Sprintf("is equal to %v", m.f)
}

type MockTaskStoreInterfaceExtended struct {
	*MockTaskStoreInterface
}

func NewMockTaskStoreInterfaceWithTx(ctrl *gomock.Controller) *MockTaskStoreInterfaceExtended {
	return &MockTaskStoreInterfaceExtended{
		MockTaskStoreInterface: NewMockTaskStoreInterface(ctrl),
	}
}

func (m *MockTaskStoreInterfaceExtended) WithTx(tx pgx.Tx) TaskStoreInterface {
	return m
}
