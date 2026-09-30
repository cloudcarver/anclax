------------------------------ MODULE Guarantees ------------------------------
EXTENDS Naturals
\* Countermodels for two unconditional promises. Every state transition is
\* enabled in production semantics; these are specification limits, not bugs.
CONSTANT Mode
VARIABLES normalDone, busy, effects, committed
vars == <<normalDone, busy, effects, committed>>
Init == /\ normalDone = FALSE /\ busy = FALSE /\ effects = 0 /\ committed = FALSE
StrictClaim == /\ Mode = "starvation" /\ ~busy /\ busy' = TRUE
               /\ UNCHANGED <<normalDone, effects, committed>>
StrictFinish == /\ busy /\ busy' = FALSE /\ UNCHANGED <<normalDone, effects, committed>>
\* Strict backlog is permanent and strictSlots=batchSize. SQL's ordered LIMIT
\* always fills the batch with strict work (AdmissionProofs!StrictSaturation).
NormalClaim == FALSE /\ normalDone' = TRUE /\ UNCHANGED <<busy, effects, committed>>
Effect == /\ Mode = "effect" /\ ~committed /\ effects < 2 /\ effects' = effects+1
          /\ UNCHANGED <<normalDone, busy, committed>>
\* A crash after the external side effect loses its completion acknowledgement.
\* Retry repeats Effect. DB lease fencing cannot undo that external effect.
Commit == /\ Mode = "effect" /\ effects > 0 /\ committed' = TRUE
          /\ UNCHANGED <<normalDone, busy, effects>>
Next == StrictClaim \/ StrictFinish \/ NormalClaim \/ Effect \/ Commit
Spec == Init /\ [][Next]_vars /\ WF_vars(StrictClaim) /\ WF_vars(StrictFinish)
NormalProgress == <>normalDone
ExactlyOnce == effects <= 1
=============================================================================
