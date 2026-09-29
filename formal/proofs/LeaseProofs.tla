------------------------------ MODULE LeaseProofs ------------------------------
EXTENDS Integers, TLAPS
CONSTANTS Workers, None
ASSUME WorkerDomain == None \notin Workers
Statuses == {"pending", "ready", "running", "paused", "cancelled", "completed", "failed"}
Controls == {"paused", "cancelled"}
Outcomes == Statuses \ {"ready", "running"}
VARIABLES status, owner, epoch, resource, lease, held, txOwner, txEpoch,
          txOutcome, staleWrite, lostControl
vars == <<status, owner, epoch, resource, lease, held, txOwner, txEpoch,
          txOutcome, staleWrite, lostControl>>

Init == /\ status = "pending" /\ owner = None /\ epoch = 0 /\ resource = 0
        /\ lease = 0 /\ held = 0 /\ txOwner = None /\ txEpoch = 0
        /\ txOutcome = "completed" /\ staleWrite = 0 /\ lostControl = 0
Inv == /\ status \in Statuses /\ owner \in Workers \cup {None}
       /\ epoch \in Nat /\ resource \in Nat /\ resource <= epoch
       /\ lease \in 0..2 /\ ((owner = None) <=> (lease = 0))
       /\ ((resource > 0) <=> (owner # None \/ status = "ready"))
       /\ (status = "ready" => owner = None /\ resource = epoch)
       /\ (status = "running" => owner # None)
       /\ (status \in {"completed", "failed"} => owner = None)
       /\ held \in 0..1 /\ txOwner \in Workers \cup {None}
       /\ txEpoch \in Nat /\ txOutcome \in Outcomes
       /\ (held = 1 => owner # None /\ owner = txOwner /\ epoch = txEpoch)
       /\ staleWrite = 0 /\ lostControl = 0

Prepare ==
    /\ held = 0 /\ status = "pending" /\ lease # 1
    /\ status' = "ready" /\ owner' = None /\ lease' = 0
    /\ epoch' = epoch+1 /\ resource' = epoch+1
    /\ UNCHANGED <<held, txOwner, txEpoch, txOutcome, staleWrite, lostControl>>
ClaimReady(w) ==
    /\ held = 0 /\ status = "ready"
    /\ status' = "running" /\ owner' = w /\ lease' = 1
    /\ UNCHANGED <<epoch, resource, held, txOwner, txEpoch, txOutcome, staleWrite, lostControl>>
ClaimPending(w) ==
    /\ held = 0 /\ status = "pending" /\ lease # 1
    /\ owner' = w /\ lease' = 1 /\ epoch' = epoch+1 /\ resource' = epoch+1
    /\ UNCHANGED <<status, held, txOwner, txEpoch, txOutcome, staleWrite, lostControl>>
Expire ==
    /\ lease = 1 /\ lease' = 2
    /\ UNCHANGED <<status, owner, epoch, resource, held, txOwner, txEpoch, txOutcome, staleWrite, lostControl>>
Clear ==
    /\ held = 0 /\ owner # None
    /\ status' = IF status = "running" THEN "pending" ELSE status
    /\ owner' = None /\ lease' = 0 /\ resource' = 0
    /\ UNCHANGED <<epoch, held, txOwner, txEpoch, txOutcome, staleWrite, lostControl>>
Recover == lease = 2 /\ Clear
Release(w,v) == epoch = v /\ owner = w /\ Clear
Renew(w,v) == held = 0 /\ owner = w /\ epoch = v /\ lease = 1 /\ UNCHANGED vars
Control(target) ==
    /\ held = 0 /\ status \in {"pending", "ready", "running", "paused", "cancelled"}
    /\ status' = target
    /\ epoch' = IF status = "ready" THEN epoch+1 ELSE epoch
    /\ resource' = IF status = "ready" THEN 0 ELSE resource
    /\ UNCHANGED <<owner, lease, held, txOwner, txEpoch, txOutcome, staleWrite, lostControl>>
Resume ==
    /\ held = 0 /\ status = "paused" /\ status' = "pending" /\ epoch' = epoch+1
    /\ UNCHANGED <<owner, resource, lease, held, txOwner, txEpoch, txOutcome, staleWrite, lostControl>>
EditReady ==
    /\ held = 0 /\ status = "ready" /\ status' = "pending"
    /\ epoch' = epoch+1 /\ resource' = 0
    /\ UNCHANGED <<owner, lease, held, txOwner, txEpoch, txOutcome, staleWrite, lostControl>>
Begin(w, v, out) ==
    /\ held = 0 /\ owner = w /\ epoch = v
    /\ status \in {"pending", "ready", "running", "paused", "cancelled"}
    /\ held' = 1 /\ txOwner' = w /\ txEpoch' = v /\ txOutcome' = out
    /\ UNCHANGED <<status, owner, epoch, resource, lease, staleWrite, lostControl>>
Commit ==
    /\ held = 1
    /\ status' = IF status \in Controls THEN status ELSE txOutcome
    /\ staleWrite' = IF owner # txOwner \/ epoch # txEpoch THEN 1 ELSE staleWrite
    /\ lostControl' = IF status \in Controls /\ status' # status THEN 1 ELSE lostControl
    /\ owner' = None /\ lease' = 0 /\ resource' = 0 /\ held' = 0
    /\ UNCHANGED <<epoch, txOwner, txEpoch, txOutcome>>
Rollback ==
    /\ held = 1 /\ held' = 0
    /\ UNCHANGED <<status, owner, epoch, resource, lease, txOwner, txEpoch, txOutcome, staleWrite, lostControl>>
Next == \/ Prepare \/ Expire \/ Recover \/ Resume \/ EditReady \/ Commit \/ Rollback
        \/ \E w \in Workers : ClaimReady(w) \/ ClaimPending(w)
        \/ \E target \in Controls : Control(target)
        \/ \E w \in Workers, v \in Nat, out \in Outcomes : Begin(w, v, out)
        \/ \E w \in Workers, v \in Nat : Release(w,v) \/ Renew(w,v)
Spec == Init /\ [][Next]_vars

THEOREM InitialInvariant == Init => Inv
BY WorkerDomain, SMT DEF Init, Inv, Statuses, Controls, Outcomes

THEOREM InductiveInvariant == Inv /\ [Next]_vars => Inv'
BY WorkerDomain, SMT DEF Inv, Next, Prepare, ClaimReady, ClaimPending, Expire,
  Clear, Recover, Release, Renew, Control, Resume, EditReady, Begin, Commit, Rollback, vars, Statuses, Controls, Outcomes

THEOREM LeaseSafety == Spec => []Inv
BY InitialInvariant, InductiveInvariant, PTL DEF Spec

THEOREM MonotonicFence == Inv /\ [Next]_vars => epoch' >= epoch
BY SMT DEF Inv, Next, Prepare, ClaimReady, ClaimPending, Expire, Clear, Recover, Release, Renew, Control,
  Resume, EditReady, Begin, Commit, Rollback, vars

THEOREM ReleaseRequiresCurrentIdentity ==
    \A w \in Workers, v \in Nat : Release(w,v) => owner = w /\ epoch = v
BY SMT DEF Release

THEOREM ExpiredLeaseCannotRenew ==
    \A w \in Workers, v \in Nat : lease = 2 => ~Renew(w,v)
BY SMT DEF Renew

\* A task-local update lifts the invariant to an arbitrary set of task IDs.
\* Shared allocation requires its own invariant; this lemma does not supply it.
THEOREM PointwiseLifting ==
    \A tasks, states, good :
      \A rows \in [tasks -> states], t \in tasks, next \in states :
      ((\A id \in tasks : rows[id] \in good) /\ next \in good) =>
        \A id \in tasks : [rows EXCEPT ![t] = next][id] \in good
BY SMT

THEOREM OtherTaskUnchanged ==
    \A tasks, states :
      \A rows \in [tasks -> states], t \in tasks, next \in states :
      \A other \in tasks \ {t} : [rows EXCEPT ![t] = next][other] = rows[other]
BY SMT
=============================================================================
