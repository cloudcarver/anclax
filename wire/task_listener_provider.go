package wire

import (
	"github.com/cloudcarver/anclax/pkg/app/closer"
	tasklistener "github.com/cloudcarver/anclax/pkg/taskcore/listener"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
)

func NewTaskEventListener(m model.ModelInterface, cm *closer.CloserManager) tasklistener.TaskEventListener {
	l := tasklistener.NewPollingTaskEventListener(m)
	cm.Register(l.Close)
	return l
}
