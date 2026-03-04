package fsm

import (
	"context"

	"github.com/cloudcarver/anclax/core"
)

type (
	SubmitPreFlopPlayerActionNextState string
	SubmitFlopPlayerActionNextState    string
	SubmitTurnPlayerActionNextState    string
	SubmitRiverPlayerActionNextState   string
)

type TexasHoldemGame interface {
	SubmitPreFlopPlayerAction(ctx context.Context, tx core.Tx) (SubmitPreFlopPlayerActionNextState, error)
	SubmitFlopPlayerAction(ctx context.Context, tx core.Tx) (SubmitFlopPlayerActionNextState, error)
	SubmitTurnPlayerAction(ctx context.Context, tx core.Tx) (SubmitTurnPlayerActionNextState, error)
	SubmitRiverPlayerAction(ctx context.Context, tx core.Tx) (SubmitRiverPlayerActionNextState, error)
}

type (
	CheckNextState      string
	CallNextState       string
	RaiseNextState      string
	FoldNextState       string
	NextStreetNextState string
)

type CheckNextStates struct{}

func (n *CheckNextStates) Checked() *CheckNextState {
	return p(CheckNextState("Checked"))
}

type CallNextStates struct{}

func (n *CallNextStates) Called() *CallNextState {
	return p(CallNextState("Called"))
}

type RaiseNextStates struct{}

func (n *RaiseNextStates) Raised() *RaiseNextState {
	return p(RaiseNextState("Raised"))
}

type FoldNextStates struct{}

func (n *FoldNextStates) Folded() *FoldNextState {
	return p(FoldNextState("Folded"))
}

type NextStreetNextStates struct{}

func (n *NextStreetNextStates) Waiting() *NextStreetNextState {
	return p(NextStreetNextState("Waiting"))
}

type TexasHoldemPlayerActions interface {
	Check(ctx context.Context, tx core.Tx, s *CheckNextStates) (*CheckNextState, error)
	Call(ctx context.Context, tx core.Tx, s *CallNextStates) (*CallNextState, error)
	Raise(ctx context.Context, tx core.Tx, s *RaiseNextStates) (*RaiseNextState, error)
	Fold(ctx context.Context, tx core.Tx, s *FoldNextStates) (*FoldNextState, error)
	NextStreet(ctx context.Context, tx core.Tx, s *NextStreetNextStates) (*NextStreetNextState, error)
}

type Repo struct {
	TexasHoldemPlayerRepo *TexasHoldemPlayerRepo
}

func NewRepo() *Repo {
	return &Repo{
		TexasHoldemPlayerRepo: &TexasHoldemPlayerRepo{},
	}
}

type TexasHoldemPlayerRepo struct {
}

func (x *TexasHoldemPlayerRepo) GetByID(ctx context.Context, id string) (*TexasHoldemPlayer, error) {
	// do stuff
	return &TexasHoldemPlayer{}, nil
}

type TexasHoldemPlayer struct {
}

func (x *TexasHoldemPlayer) Check(ctx context.Context) error {
	// do stuff
	return s.Checked(), nil
}

func (x *TexasHoldemPlayer) CheckWithTx(ctx context.Context, tx core.Tx) error {
	// do stuff
	return s.Checked(), nil
}

func p[T any](v T) *T {
	return &v
}

type Store interface {
	Write(ctx context.Context, tx core.Tx, player *TexasHoldemPlayer) error
}
