------------------------------- MODULE Progress -------------------------------
EXTENDS Naturals, FiniteSets
CONSTANTS Tasks, Capacity, Fair, FinishCalls, AllowCrashes
VARIABLES stage, abandoned
vars == <<stage, abandoned>>
Init == stage = [t \in Tasks |-> "pending"] /\ abandoned = {}
Active == {t \in Tasks : stage[t] \in {"ready", "running", "finalizing"}}
Admit(t) == stage[t] = "pending" /\ Cardinality(Active) < Capacity
           /\ stage' = [stage EXCEPT ![t] = "ready"] /\ UNCHANGED abandoned
Claim(t) == stage[t] = "ready" /\ stage' = [stage EXCEPT ![t] = "running"] /\ UNCHANGED abandoned
Execute(t) == FinishCalls /\ stage[t] = "running"
             /\ stage' = [stage EXCEPT ![t] = "finalizing"] /\ UNCHANGED abandoned
Finalize(t) == FinishCalls /\ stage[t] = "finalizing"
              /\ stage' = [stage EXCEPT ![t] = "done"] /\ UNCHANGED abandoned
Crash(t) == AllowCrashes /\ stage[t] = "running"
           /\ stage' = [stage EXCEPT ![t] = "pending"] /\ abandoned' = abandoned \cup {t}
Next == \E t \in Tasks : Admit(t) \/ Claim(t) \/ Execute(t) \/ Finalize(t) \/ Crash(t)
\* SF is per task/action, not merely fair scheduling of some task. The finite
\* workload and positive capacity make waiting tasks eligible again. In a
\* system with arrivals this assumption must not be silently inferred.
Fairness == \A t \in Tasks : SF_vars(Admit(t)) /\ SF_vars(Claim(t))
                           /\ SF_vars(Execute(t)) /\ WF_vars(Finalize(t))
Spec == Init /\ [][Next]_vars /\ IF Fair THEN Fairness ELSE TRUE
TypeOK == stage \in [Tasks -> {"pending", "ready", "running", "finalizing", "done"}] /\ abandoned \subseteq Tasks
CapacitySafe == Cardinality(Active) <= Capacity
EventuallyDone == <> (\A t \in Tasks : stage[t] = "done")
=============================================================================
