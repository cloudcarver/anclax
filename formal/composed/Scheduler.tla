------------------------------- MODULE Scheduler -------------------------------
EXTENDS Integers, FiniteSets
CONSTANTS TaskCount, TagCount, SlotCount, Workers, MaxVersion, None, Capacity,
          StrictTasks, Serial, CheckBatchBound, CheckSchedulerFence, CheckSerial
Tasks == 1..TaskCount
Needs == [t \in Tasks |-> 1..TagCount]
VARIABLES rows, permits, issued, finalizing, staleFinalization, overwrittenControl,
          owners, active, limits, resized, phase, position, candidate, pending,
          guards, overflowGuard, badAdmission,
          cycle, reserved, strictReserved, batchPhase, buffer, stopped,
          schedulerOwner, schedulerEpoch, schedulerValid, schedulerIssued,
          preparing, prepareToken, stalePreparation
L == INSTANCE TaskLease WITH FenceFinalize <- TRUE, PreserveControl <- TRUE, ReleaseOnlyOwn <- TRUE
S == INSTANCE TagSlots WITH AllowResize <- TRUE, RecheckSlot <- TRUE,
       RollbackPartial <- TRUE, CheckOverflow <- TRUE, SerializeOverflow <- TRUE,
       ConfigurationBarrier <- TRUE
leaseVars == <<rows, permits, issued, finalizing, staleFinalization, overwrittenControl>>
slotVars == <<owners, active, limits, resized, phase, position, candidate, pending,
              guards, overflowGuard, badAdmission>>
local == <<cycle, reserved, strictReserved, batchPhase, buffer, stopped>>
scheduler == <<schedulerOwner, schedulerEpoch, schedulerValid, schedulerIssued,
               preparing, prepareToken, stalePreparation>>
vars == <<leaseVars, slotVars, local, scheduler>>
Attempt == [task : Tasks, worker : Workers, version : 1..MaxVersion]
Tokens == [worker : Workers, version : 1..MaxVersion]
Live(w) == {a \in Attempt : a.worker = w /\ cycle[a] # "idle"}
Strict(as) == {a \in as : a.task \in StrictTasks}
Used(w) == Cardinality(Live(w))+reserved[w]
Occupies(t) == rows[t].status = "ready" \/ rows[t].lease = "valid"
RowFree(t) == phase[t] = "idle" /\ L!Unlocked(t)

Init == /\ L!Init /\ S!Init
        /\ cycle = [a \in Attempt |-> "idle"]
        /\ reserved = [w \in Workers |-> 0] /\ strictReserved = [w \in Workers |-> 0]
        /\ batchPhase = [w \in Workers |-> "idle"] /\ buffer = [w \in Workers |-> {}]
        /\ stopped = {}
        /\ schedulerOwner = None /\ schedulerEpoch = 0 /\ schedulerValid = FALSE
        /\ schedulerIssued = {} /\ preparing = None /\ prepareToken = None
        /\ stalePreparation = FALSE

TakeScheduler(w) ==
    /\ ~schedulerValid /\ preparing = None /\ schedulerEpoch < MaxVersion
    /\ schedulerOwner' = w /\ schedulerEpoch' = schedulerEpoch+1 /\ schedulerValid' = TRUE
    /\ schedulerIssued' = schedulerIssued \cup {[worker |-> w, version |-> schedulerEpoch+1]}
    /\ UNCHANGED <<leaseVars, slotVars, local, preparing, prepareToken, stalePreparation>>
ExpireScheduler ==
    /\ schedulerValid' = FALSE
    /\ UNCHANGED <<leaseVars, slotVars, local, schedulerOwner, schedulerEpoch,
                   schedulerIssued, preparing, prepareToken, stalePreparation>>
BeginPrepare(t, token) ==
    /\ preparing = None /\ RowFree(t) /\ rows[t].status = "pending"
    /\ rows[t].lease = None /\ rows[t].version < MaxVersion
    /\ (~CheckSchedulerFence \/ (schedulerValid /\ token.worker = schedulerOwner /\ token.version = schedulerEpoch))
    /\ (~Serial \/ ~CheckSerial \/ \A other \in Tasks \ {t} : ~Occupies(other))
    /\ (~Serial \/ ~\E earlier \in 1..(t-1) : rows[earlier].status \in {"pending", "ready"})
    /\ S!Begin(t) /\ preparing' = t /\ prepareToken' = token
    /\ stalePreparation' = (stalePreparation \/ ~schedulerValid \/ token.worker # schedulerOwner \/ token.version # schedulerEpoch)
    /\ UNCHANGED <<leaseVars, local, schedulerOwner, schedulerEpoch, schedulerValid, schedulerIssued>>
AllocationStep(t) ==
    /\ t = preparing
    /\ \/ S!SkipTag(t) \/ S!InspectOverflow(t) \/ S!TakeOverflowGuard(t)
       \/ S!CountOverflow(t) \/ S!TakeSlotGuard(t) \/ S!WriteSlot(t)
       \/ \E slot \in S!Slots : S!Scan(t, slot)
    /\ UNCHANGED <<leaseVars, local, scheduler>>
FinishPrepare(t) ==
    /\ preparing = t /\ S!Commit(t) /\ L!Prepare(t)
    /\ preparing' = None /\ prepareToken' = None
    /\ UNCHANGED <<local, schedulerOwner, schedulerEpoch, schedulerValid, schedulerIssued, stalePreparation>>
AbortPrepare(t) ==
    /\ preparing = t /\ S!Abort(t) /\ preparing' = None /\ prepareToken' = None
    /\ UNCHANGED <<leaseVars, local, schedulerOwner, schedulerEpoch, schedulerValid, schedulerIssued, stalePreparation>>
DropSlots(t) == IF t \in active THEN S!Release(t) ELSE UNCHANGED slotVars
Expire(t) == L!Expire(t) /\ UNCHANGED <<slotVars, local, scheduler>>
Recover(t) == RowFree(t) /\ L!Recover(t) /\ DropSlots(t) /\ UNCHANGED <<local, scheduler>>
Control(t, status) ==
    /\ RowFree(t) /\ L!Control(t, status)
    /\ IF rows[t].status = "ready" THEN DropSlots(t) ELSE UNCHANGED slotVars
    /\ UNCHANGED <<local, scheduler>>
Resume(t) == RowFree(t) /\ L!Resume(t) /\ UNCHANGED <<slotVars, local, scheduler>>
Edit(t) == RowFree(t) /\ L!EditReady(t) /\ DropSlots(t) /\ UNCHANGED <<local, scheduler>>
Release(a) == RowFree(a.task) /\ L!Release(a) /\ DropSlots(a.task) /\ UNCHANGED <<local, scheduler>>

\* Repeat arbitrary increases/decreases within the finite slot universe.
\* The task-table configuration barrier drains BOTH allocation and finalize.
Configure(g, n) ==
    /\ \A t \in Tasks : RowFree(t)
    /\ owners' = [p \in S!Places |-> IF p[1] = g THEN S!Packed(g, p[2]) ELSE owners[p]]
    /\ limits' = [limits EXCEPT ![g] = n] /\ resized' = TRUE
    /\ UNCHANGED <<active, phase, position, candidate, pending, guards, overflowGuard, badAdmission,
                   leaseVars, local, scheduler>>

BeginBatch(w, n, s) ==
    /\ w \notin stopped /\ batchPhase[w] = "idle" /\ n > 0
    /\ Used(w)+n <= Capacity /\ s <= n
    /\ reserved' = [reserved EXCEPT ![w] = n] /\ strictReserved' = [strictReserved EXCEPT ![w] = s]
    /\ batchPhase' = [batchPhase EXCEPT ![w] = "query"]
    /\ UNCHANGED <<leaseVars, slotVars, scheduler, cycle, buffer, stopped>>
DBBatch(w, chosen) ==
    /\ batchPhase[w] = "query"
    /\ \A t \in chosen : RowFree(t) /\ rows[t].status = "ready"
    /\ (~CheckBatchBound \/ Cardinality(chosen) <= reserved[w])
    /\ Cardinality(chosen \cap StrictTasks) <= strictReserved[w]
    /\ rows' = [t \in Tasks |-> IF t \in chosen THEN
          [rows[t] EXCEPT !.status = "running", !.owner = w, !.lease = "valid"] ELSE rows[t]]
    /\ LET results == {[task |-> t, worker |-> w, version |-> rows[t].version] : t \in chosen}
       IN /\ issued' = issued \cup results /\ buffer' = [buffer EXCEPT ![w] = results]
    /\ batchPhase' = [batchPhase EXCEPT ![w] = "reply"]
    /\ UNCHANGED <<permits, finalizing, staleFinalization, overwrittenControl, slotVars, scheduler,
                   cycle, reserved, strictReserved, stopped>>
Receive(w) ==
    /\ batchPhase[w] = "reply"
    /\ cycle' = [a \in Attempt |-> IF a \in buffer[w] THEN "executing" ELSE cycle[a]]
    /\ buffer' = [buffer EXCEPT ![w] = {}] /\ batchPhase' = [batchPhase EXCEPT ![w] = "idle"]
    /\ reserved' = [reserved EXCEPT ![w] = 0] /\ strictReserved' = [strictReserved EXCEPT ![w] = 0]
    /\ UNCHANGED <<leaseVars, slotVars, scheduler, stopped>>
Execute(a) ==
    /\ cycle[a] = "executing" /\ cycle' = [cycle EXCEPT ![a] = "finalizing"]
    /\ UNCHANGED <<leaseVars, slotVars, scheduler, reserved, strictReserved, batchPhase, buffer, stopped>>
BeginFinalize(a, outcome) ==
    /\ cycle[a] = "finalizing" /\ RowFree(a.task) /\ L!BeginFinalize(a, outcome)
    /\ UNCHANGED <<slotVars, local, scheduler>>
CommitFinalize(t) ==
    /\ finalizing[t] # None /\ L!CommitFinalize(t) /\ DropSlots(t)
    /\ cycle' = [cycle EXCEPT ![finalizing[t].attempt] = "idle"]
    /\ UNCHANGED <<scheduler, reserved, strictReserved, batchPhase, buffer, stopped>>
RollbackFinalize(t) == L!RollbackFinalize(t) /\ UNCHANGED <<slotVars, local, scheduler>>
Abandon(a) ==
    /\ cycle[a] = "finalizing" /\ L!Unlocked(a.task)
    /\ cycle' = [cycle EXCEPT ![a] = "idle"]
    /\ UNCHANGED <<leaseVars, slotVars, scheduler, reserved, strictReserved, batchPhase, buffer, stopped>>
Stop(w) == stopped' = stopped \cup {w} /\ UNCHANGED <<leaseVars, slotVars, scheduler, cycle, reserved, strictReserved, batchPhase, buffer>>
Next ==
    \/ ExpireScheduler
    \/ \E w \in Workers :
         \/ TakeScheduler(w) \/ Receive(w) \/ Stop(w)
         \/ \E n, s \in 0..Capacity : BeginBatch(w, n, s)
         \/ \E chosen \in SUBSET Tasks : DBBatch(w, chosen)
    \/ \E t \in Tasks :
         \/ AllocationStep(t) \/ FinishPrepare(t) \/ AbortPrepare(t)
         \/ Expire(t) \/ Recover(t) \/ Resume(t) \/ Edit(t)
         \/ CommitFinalize(t) \/ RollbackFinalize(t)
         \/ \E token \in schedulerIssued : BeginPrepare(t, token)
         \/ \E status \in L!ControlStates : Control(t, status)
    \/ \E a \in issued :
         \/ Execute(a) \/ Release(a) \/ Abandon(a)
         \/ \E outcome \in L!Outcomes : BeginFinalize(a, outcome)
    \/ \E g \in S!Tags, n \in 0..SlotCount : Configure(g, n)
Spec == Init /\ [][Next]_vars

\* Conditional liveness on THIS composed protocol: finite normal workload,
\* positive fixed slots, stable scheduler/Workers, no control edits or failures,
\* per-task fair admission/selection and eventually returning commands. These
\* are explicit restrictions, not properties inferred about an arbitrary run.
StartPrepare(t) == \E token \in schedulerIssued : BeginPrepare(t, token)
StartBatch(w) == \E n \in 1..Capacity : BeginBatch(w,n,0)
ReturnBatch(w) == \E chosen \in SUBSET Tasks : DBBatch(w,chosen)
SelectTask(w,t) == \E chosen \in SUBSET Tasks : t \in chosen /\ DBBatch(w,chosen)
CompleteAttempt(a) == BeginFinalize(a,"completed")
CompletionNext ==
    \/ \E w \in Workers : TakeScheduler(w) \/ StartBatch(w) \/ ReturnBatch(w) \/ Receive(w)
    \/ \E t \in Tasks : StartPrepare(t) \/ AllocationStep(t) \/ FinishPrepare(t) \/ CommitFinalize(t)
    \/ \E a \in issued : Execute(a) \/ CompleteAttempt(a)
CompletionSpec ==
    /\ Init /\ [][CompletionNext]_vars
    /\ WF_vars(\E w \in Workers : TakeScheduler(w))
    /\ \A t \in Tasks : SF_vars(StartPrepare(t)) /\ WF_vars(AllocationStep(t))
                         /\ WF_vars(FinishPrepare(t)) /\ WF_vars(CommitFinalize(t))
    /\ \A w \in Workers : WF_vars(StartBatch(w)) /\ WF_vars(ReturnBatch(w)) /\ WF_vars(Receive(w))
    /\ \A w \in Workers, t \in Tasks : SF_vars(SelectTask(w,t))
    /\ \A a \in Attempt : WF_vars(Execute(a)) /\ WF_vars(CompleteAttempt(a))
AllCompleted == <> (\A t \in Tasks : rows[t].status = "completed" /\ permits[t] = 0 /\ t \notin active)

ProtocolType ==
    /\ cycle \in [Attempt -> {"idle", "executing", "finalizing"}]
    /\ reserved \in [Workers -> 0..Capacity] /\ strictReserved \in [Workers -> 0..Capacity]
    /\ batchPhase \in [Workers -> {"idle", "query", "reply"}]
    /\ buffer \in [Workers -> SUBSET Attempt] /\ stopped \subseteq Workers
    /\ schedulerOwner \in Workers \cup {None} /\ schedulerEpoch \in 0..MaxVersion
    /\ schedulerValid \in BOOLEAN /\ schedulerIssued \subseteq Tokens
    /\ preparing \in Tasks \cup {None} /\ prepareToken \in Tokens \cup {None}
    /\ stalePreparation \in BOOLEAN
BatchLifecycle == \A w \in Workers :
    /\ (reserved[w] = 0) <=> (batchPhase[w] = "idle")
    /\ strictReserved[w] <= reserved[w]
    /\ buffer[w] \subseteq issued
    /\ \A a \in buffer[w] : a.worker = w /\ cycle[a] = "idle"
    /\ (batchPhase[w] # "reply" => buffer[w] = {})
LeaseSafety == L!TypeOK /\ L!NoStaleFinalize /\ L!ControlWins /\ L!ResourcesAccounted /\ L!LeaseShape /\ L!IssuedVersions
SlotSafety == S!TypeOK /\ S!AllOrNothing /\ S!PrivateWritesGuarded /\ S!NoNewOversubscription
Coupling == \A t \in Tasks : (permits[t] > 0) <=> (t \in active)
BudgetSafety == \A w \in Workers : Used(w) <= Capacity
ResponseContract == \A w \in Workers : Cardinality(buffer[w]) <= reserved[w] /\ Cardinality(Strict(buffer[w])) <= strictReserved[w]
KnownAttempts == \A a \in Attempt : cycle[a] # "idle" => a \in issued
SchedulerFence == ~stalePreparation
SerialSafety == ~Serial \/ Cardinality({t \in Tasks : Occupies(t)}) <= 1
NoOldExecution == ~\E a \in Attempt : cycle[a] = "executing" /\ a.version < rows[a.task].version
=============================================================================
