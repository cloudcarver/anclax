package app

import "github.com/cloudcarver/anchor/pkg/zcore/model"

type Closer struct {
	model model.ModelInterface
}

func NewCloser(model model.ModelInterface) *Closer {
	return &Closer{
		model: model,
	}
}

func (c *Closer) Close() {
	c.model.Close()
}
