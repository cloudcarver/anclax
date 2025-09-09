package app

import "github.com/cloudcarver/anchor/pkg/zcore/model"

type Closer struct {
	closers []func()
}

func NewCloser(model model.ModelInterface) *Closer {
	closers := []func(){
		model.Close,
	}

	return &Closer{
		closers: closers,
	}
}
