State Machine

Use yaml to define state machine:

```yaml
Cluster:
   states:
      Creating:
         allowActions: [StartDelete]
      Created:
         allowActions: [StartDelete]
      Deleting:
         allowActions: [DoDelete]
   actions:
      StartDelete:
         next: [Deleting]
      DoDelete:
         next: [Deleted]
```

Generated interfaces:

```go
type ClusterStateMachine interface {
   StartDelete(ctx context.Context, tx core.Tx) (StartDeleteNextState, error)
   DoDelete(ctx context.Context, tx core.Tx) (DoDeleteNextState, error)
   PostDelete(ctx context.Context, tx core.Tx) (PostDeleteNextState, error)
}
```

```yaml
TexasHoldemGame:
   store:
      pg:
         table: texas_holdem_games
   states:
      PreFlop:
         allowActions: [SubmitPreFlopPlayerAction]
      Flop:
         allowActions: [SubmitFlopPlayerAction]
      Turn:
         allowActions: [SubmitTurnPlayerAction]
      River:
         allowActions: [SubmitRiverPlayerAction]
      Showdown:
   actions:
      SubmitPreFlopPlayerAction:
         next: [Flop]
      SubmitFlopPlayerAction:
         next: [Turn]
      SubmitTurnPlayerAction:
         next: [River]
      SubmitRiverPlayerAction:
         next: [Showdown]

TexasHoldemPlayer:
   states:
      Waiting:
         allowActions: [CanAct]
      Acting:
         allowActions: [Check, Call, Raise, Fold]
      Checked:
         allowActions: [NextStreet]
      Called:
         allowActions: [NextStreet]
      Raised:
         allowActions: [NextStreet]
      Folded:
         allowActions: [NextStreet]
   actions:
      Check:
         next: [Checked]
      Call:
         next: [Called]
      Raise:
         next: [Raised]
      Fold:
         next: [Folded]
      NextStreet:
         next: [Waiting]
```

Generated interfaces:

```go


```
