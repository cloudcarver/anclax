---------------------------- MODULE WorkerBudget ----------------------------
EXTENDS Integers, FiniteSets
CONSTANTS Capacity, ControlCapacity, BatchSize, MaxIDs,
          ReserveBatch, HoldFinalizing, GuardPhase, RespectStop, KeepFallback
IDs == 1..MaxIDs
Lanes == {"normal", "strict", "control"}
ClaimPhases == {"strictClaim", "normalClaim", "directClaim"}
LivePhases == ClaimPhases \cup {"executing", "finalizing"}
Phases == LivePhases \cup {"unused", "done"}
Min(a, b) == IF a < b THEN a ELSE b
Max(a, b) == IF a > b THEN a ELSE b

VARIABLES phase, lane, nextID, batchID, batchSize, batchStrict,
          inFlight, strictInFlight, controlInFlight, strictCap, stopped,
          admittedAfterStop
vars == <<phase, lane, nextID, batchID, batchSize, batchStrict,
          inFlight, strictInFlight, controlInFlight, strictCap, stopped,
          admittedAfterStop>>

Init ==
    /\ phase = [i \in IDs |-> "unused"] /\ lane = [i \in IDs |-> "normal"]
    /\ nextID = 0 /\ batchID = 0 /\ batchSize = 0 /\ batchStrict = 0
    /\ inFlight = 0 /\ strictInFlight = 0 /\ controlInFlight = 0
    /\ strictCap = Capacity /\ stopped = FALSE /\ admittedAfterStop = FALSE

CanStart == (~RespectStop \/ ~stopped) /\ nextID < MaxIDs
Business == {i \in IDs : phase[i] \in LivePhases /\ lane[i] # "control"}
Strict == {i \in Business : lane[i] = "strict"}
Control == {i \in IDs : phase[i] \in LivePhases /\ lane[i] = "control"}

BeginBatch ==
    /\ CanStart /\ batchID = 0 /\ inFlight < Capacity
    /\ LET n == Min(BatchSize, Capacity-inFlight)
           s == Min(n, Max(0, strictCap-strictInFlight))
       IN /\ batchSize' = n /\ batchStrict' = s
          /\ inFlight' = inFlight + IF ReserveBatch THEN n ELSE 0
          /\ strictInFlight' = strictInFlight + IF ReserveBatch THEN s ELSE 0
    /\ batchID' = nextID+1 /\ nextID' = nextID+1
    /\ admittedAfterStop' = (admittedAfterStop \/ stopped)
    /\ UNCHANGED <<phase, lane, controlInFlight, strictCap, stopped>>

\* The Port contract is an explicit assumption: 0 <= n <= reserved size and
\* 0 <= s <= min(n,reserved strict slots). Config may shrink while SQL is pending.
BatchResult(n, s) ==
    /\ batchID # 0 /\ n <= batchSize /\ s <= n /\ s <= batchStrict
    /\ nextID+n <= MaxIDs
    /\ phase' = [i \in IDs |-> IF i \in (nextID+1)..(nextID+n) THEN "executing" ELSE phase[i]]
    /\ lane' = [i \in IDs |-> IF i \in (nextID+1)..(nextID+n)
         THEN IF i <= nextID+s THEN "strict" ELSE "normal" ELSE lane[i]]
    /\ nextID' = nextID+n /\ batchID' = 0 /\ batchSize' = 0 /\ batchStrict' = 0
    /\ inFlight' = inFlight-batchSize+n
    /\ strictInFlight' = strictInFlight-batchStrict+s
    /\ UNCHANGED <<controlInFlight, strictCap, stopped, admittedAfterStop>>

\* Single claims abstract admitted manual requests and the non-batch poll path.
\* Queue order/label weights are omitted; every permitted admission is explored.
BeginSingle(l, p) ==
    /\ CanStart
    /\ IF l = "control" THEN controlInFlight < ControlCapacity
       ELSE /\ inFlight < Capacity
            /\ l = "strict" => strictInFlight < strictCap
    /\ (p = "strictClaim" => l = "strict")
    /\ (p = "normalClaim" => l = "normal")
    /\ phase' = [phase EXCEPT ![nextID+1] = p]
    /\ lane' = [lane EXCEPT ![nextID+1] = l] /\ nextID' = nextID+1
    /\ inFlight' = inFlight + IF l = "control" THEN 0 ELSE 1
    /\ strictInFlight' = strictInFlight + IF l = "strict" THEN 1 ELSE 0
    /\ controlInFlight' = controlInFlight + IF l = "control" THEN 1 ELSE 0
    /\ admittedAfterStop' = (admittedAfterStop \/ stopped)
    /\ UNCHANGED <<batchID, batchSize, batchStrict, strictCap, stopped>>

Fallback(i) ==
    /\ phase[i] = "strictClaim"
    /\ phase' = [phase EXCEPT ![i] = "normalClaim"]
    /\ strictInFlight' = strictInFlight - IF KeepFallback THEN 0 ELSE 1
    /\ UNCHANGED <<lane, nextID, batchID, batchSize, batchStrict,
                   inFlight, controlInFlight, strictCap, stopped, admittedAfterStop>>

SingleSuccess(i, isStrict) ==
    /\ phase[i] \in ClaimPhases
    /\ (isStrict => lane[i] = "strict")
    /\ (phase[i] = "strictClaim" => isStrict)
    /\ phase' = [phase EXCEPT ![i] = "executing"]
    /\ lane' = [lane EXCEPT ![i] = IF @ = "strict" /\ ~isStrict THEN "normal" ELSE @]
    /\ strictInFlight' = strictInFlight - IF lane[i] = "strict" /\ ~isStrict THEN 1 ELSE 0
    /\ UNCHANGED <<nextID, batchID, batchSize, batchStrict, inFlight,
                   controlInFlight, strictCap, stopped, admittedAfterStop>>

Finish(i) ==
    /\ phase' = [phase EXCEPT ![i] = "done"]
    /\ inFlight' = inFlight - IF lane[i] = "control" THEN 0 ELSE 1
    /\ strictInFlight' = strictInFlight - IF lane[i] = "strict" THEN 1 ELSE 0
    /\ controlInFlight' = controlInFlight - IF lane[i] = "control" THEN 1 ELSE 0
    /\ UNCHANGED <<lane, nextID, batchID, batchSize, batchStrict,
                   strictCap, stopped, admittedAfterStop>>

ClaimFailure(i) == phase[i] \in ClaimPhases /\ Finish(i)
FinalizeResult(i) ==
    /\ (phase[i] = "finalizing" \/ (~GuardPhase /\ phase[i] = "done"))
    /\ Finish(i)

ExecuteResult(i) ==
    /\ phase[i] = "executing"
    /\ phase' = [phase EXCEPT ![i] = "finalizing"]
    /\ inFlight' = inFlight - IF ~HoldFinalizing /\ lane[i] # "control" THEN 1 ELSE 0
    /\ strictInFlight' = strictInFlight - IF ~HoldFinalizing /\ lane[i] = "strict" THEN 1 ELSE 0
    /\ controlInFlight' = controlInFlight - IF ~HoldFinalizing /\ lane[i] = "control" THEN 1 ELSE 0
    /\ UNCHANGED <<lane, nextID, batchID, batchSize, batchStrict,
                   strictCap, stopped, admittedAfterStop>>

SetCap(cap) ==
    /\ strictCap' = cap
    /\ UNCHANGED <<phase, lane, nextID, batchID, batchSize, batchStrict,
                   inFlight, strictInFlight, controlInFlight, stopped, admittedAfterStop>>
Stop ==
    /\ stopped' = TRUE
    /\ UNCHANGED <<phase, lane, nextID, batchID, batchSize, batchStrict,
                   inFlight, strictInFlight, controlInFlight, strictCap, admittedAfterStop>>

Next ==
    \/ BeginBatch \/ Stop
    \/ \E n \in 0..BatchSize, s \in 0..BatchSize : BatchResult(n, s)
    \/ \E l \in Lanes, p \in ClaimPhases : BeginSingle(l, p)
    \/ \E i \in IDs :
         \/ Fallback(i) \/ ClaimFailure(i) \/ ExecuteResult(i) \/ FinalizeResult(i)
         \/ \E b \in BOOLEAN : SingleSuccess(i, b)
    \/ \E cap \in 0..Capacity : SetCap(cap)
Spec == Init /\ [][Next]_vars

TypeOK ==
    /\ phase \in [IDs -> Phases] /\ lane \in [IDs -> Lanes]
    /\ nextID \in 0..MaxIDs /\ batchID \in 0..MaxIDs
    /\ batchSize \in 0..BatchSize /\ batchStrict \in 0..BatchSize
    /\ inFlight \in Int /\ strictInFlight \in Int /\ controlInFlight \in Int
    /\ strictCap \in 0..Capacity /\ stopped \in BOOLEAN /\ admittedAfterStop \in BOOLEAN
BudgetAccounting ==
    /\ inFlight = Cardinality(Business) + batchSize
    /\ strictInFlight = Cardinality(Strict) + batchStrict
    /\ controlInFlight = Cardinality(Control)
CapacitySafe ==
    /\ inFlight \in 0..Capacity /\ strictInFlight \in 0..inFlight
    /\ controlInFlight \in 0..ControlCapacity
NoPostStopAdmission == ~admittedAfterStop
BatchShape == /\ (batchID = 0) <=> (batchSize = 0)
              /\ batchStrict <= batchSize
NoGrandfatheredStrict == strictInFlight <= strictCap
NoControlBesideFullBusiness == ~(inFlight = Capacity /\ controlInFlight > 0)
NoStoppedFinalization == ~(stopped /\ \E i \in IDs : phase[i] = "finalizing")
=============================================================================
