package store

import (
	"fmt"
	reflect "reflect"

	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
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
