--------------------------- MODULE SchedulerProofs ---------------------------
EXTENDS Integers, TLAPS

\* A parameterized abstraction of Engine's capacity accounting. The theorem
\* ranges over arbitrary mathematical natural capacities and execution length.
\* It is NOT a refinement proof of WorkerBudget.tla or of the Go implementation.
CONSTANTS Capacity, ControlCapacity
ASSUME CapacityAssumption == Capacity \in Nat \ {0} /\ ControlCapacity \in Nat
VARIABLES active, reserved, strictActive, strictReserved, control
vars == <<active, reserved, strictActive, strictReserved, control>>
Min(a, b) == IF a < b THEN a ELSE b
Max(a, b) == IF a > b THEN a ELSE b

Init == /\ active = 0 /\ reserved = 0 /\ strictActive = 0
        /\ strictReserved = 0 /\ control = 0
Inv == /\ active \in Nat /\ reserved \in Nat
       /\ strictActive \in Nat /\ strictReserved \in Nat /\ control \in Nat
       /\ active + reserved <= Capacity
       /\ strictActive <= active /\ strictReserved <= reserved
       /\ control <= ControlCapacity

Reserve(n, s) ==
    /\ reserved = 0 /\ n > 0 /\ active+n <= Capacity /\ s <= n
    /\ reserved' = n /\ strictReserved' = s
    /\ UNCHANGED <<active, strictActive, control>>
Receive(n, s) ==
    /\ reserved > 0 /\ n <= reserved /\ s <= n /\ s <= strictReserved
    /\ active' = active+n /\ strictActive' = strictActive+s
    /\ reserved' = 0 /\ strictReserved' = 0 /\ UNCHANGED control
Admit(strict) ==
    /\ active+reserved < Capacity
    /\ active' = active+1
    /\ strictActive' = strictActive + IF strict THEN 1 ELSE 0
    /\ UNCHANGED <<reserved, strictReserved, control>>
Finish(strict) ==
    /\ active > 0
    /\ IF strict THEN strictActive > 0 ELSE strictActive < active
    /\ active' = active-1
    /\ strictActive' = strictActive - IF strict THEN 1 ELSE 0
    /\ UNCHANGED <<reserved, strictReserved, control>>
Downgrade ==
    /\ strictActive > 0 /\ strictActive' = strictActive-1
    /\ UNCHANGED <<active, reserved, strictReserved, control>>
AdmitControl ==
    /\ control < ControlCapacity /\ control' = control+1
    /\ UNCHANGED <<active, reserved, strictActive, strictReserved>>
FinishControl ==
    /\ control > 0 /\ control' = control-1
    /\ UNCHANGED <<active, reserved, strictActive, strictReserved>>
Next ==
    \/ \E n, s \in Nat : Reserve(n, s) \/ Receive(n, s)
    \/ \E strict \in BOOLEAN : Admit(strict) \/ Finish(strict)
    \/ Downgrade \/ AdmitControl \/ FinishControl
Spec == Init /\ [][Next]_vars

THEOREM InitialInvariant == Init => Inv
BY CapacityAssumption, SMT DEF Init, Inv

THEOREM InductiveInvariant == Inv /\ [Next]_vars => Inv'
BY CapacityAssumption, SMT DEF Inv, Next, Reserve, Receive, Admit, Finish, Downgrade,
           AdmitControl, FinishControl, vars

THEOREM CapacitySafety == Spec => []Inv
BY InitialInvariant, InductiveInvariant, PTL DEF Spec

\* Connect the min/clamp formulas in batch.go to Reserve's arithmetic guards.
THEOREM BatchSizing ==
    \A c, used, batch \in Nat :
      (used < c /\ batch > 0) =>
        LET n == Min(batch, c-used)
        IN n > 0 /\ n <= batch /\ used+n <= c
BY SMT DEF Min

\* A strict-cap shrink can retain existing work, but grants zero new strict
\* reservations until usage is below the new cap. Replies consume OLD grants.
THEOREM StrictSizing ==
    \A n, used, cap \in Nat :
      LET s == Min(n, Max(0, cap-used))
      IN /\ s \in Nat /\ s <= n
         /\ (used < cap => used+s <= cap)
         /\ (used >= cap => s = 0)
BY SMT DEF Min, Max

\* Conditional lemma, NOT a proof of the slot protocol. The premise applies
\* while all admissions share the overflow guard. If the last retired owner
\* releases, other admissions may skip that guard: safety then needs a separate
\* ordinary-slot argument. TagSlots explores both paths within finite bounds.
THEOREM OverflowAdmissionBound ==
    \A limit, atCheck, atCommit \in Nat :
      (atCheck < limit /\ atCommit <= atCheck) => atCommit+1 <= limit
BY SMT
=============================================================================
