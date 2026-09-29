------------------------------ MODULE QuotaProofs ------------------------------
EXTENDS Integers, TLAPS
Min(a,b) == IF a < b THEN a ELSE b
\* Aggregate refinement of a SINGLE tag's allocation protocol. Ordinary slot
\* guards ensure used+private<=limit. While retired owners remain, admissions
\* share the overflow guard and no ordinary reservation may start. SQL's
\* statement-level implementation of these premises is checked in TagSlots.
VARIABLES limit, ordinary, retired, private, guarded, checked, unlimited, mode, bad
vars == <<limit, ordinary, retired, private, guarded, checked, unlimited, mode, bad>>
Init == /\ limit = 0 /\ ordinary = 0 /\ retired = 0 /\ private = 0
        /\ guarded = 0 /\ checked = 0 /\ unlimited = 0 /\ mode = 0 /\ bad = 0
Inv == /\ limit \in Nat /\ ordinary \in Nat /\ retired \in Nat /\ private \in Nat
       /\ guarded \in 0..1 /\ checked \in Nat /\ unlimited \in Nat /\ mode \in 0..1
       /\ ordinary+private+guarded <= limit
       /\ (retired > 0 => private = 0)
       /\ (retired > 0 /\ guarded = 1 => ordinary+retired <= checked /\ checked < limit)
       /\ (mode = 0 => ordinary = 0 /\ retired = 0 /\ private = 0 /\ guarded = 0)
       /\ (mode = 1 => unlimited = 0)
       /\ bad = 0

ReserveOrdinary ==
    /\ mode = 1 /\ retired = 0 /\ ordinary+private+guarded < limit
    /\ private' = private+1
    /\ UNCHANGED <<limit, ordinary, retired, guarded, checked, unlimited, mode, bad>>
ReserveOverflow ==
    /\ mode = 1 /\ retired > 0 /\ guarded = 0 /\ ordinary+retired < limit
    /\ guarded' = 1 /\ checked' = ordinary+retired
    /\ UNCHANGED <<limit, ordinary, retired, private, unlimited, mode, bad>>
CommitOrdinary ==
    /\ private > 0 /\ private' = private-1 /\ ordinary' = ordinary+1
    /\ bad' = IF ordinary+retired >= limit THEN 1 ELSE bad
    /\ UNCHANGED <<limit, retired, guarded, checked, unlimited, mode>>
CommitOverflow ==
    /\ guarded = 1 /\ guarded' = 0 /\ ordinary' = ordinary+1
    /\ bad' = IF ordinary+retired >= limit THEN 1 ELSE bad
    /\ UNCHANGED <<limit, retired, private, checked, unlimited, mode>>
AbortOrdinary ==
    /\ private > 0 /\ private' = private-1
    /\ UNCHANGED <<limit, ordinary, retired, guarded, checked, unlimited, mode, bad>>
AbortOverflow ==
    /\ guarded = 1 /\ guarded' = 0
    /\ UNCHANGED <<limit, ordinary, retired, private, checked, unlimited, mode, bad>>
ReleaseOrdinary ==
    /\ ordinary > 0 /\ ordinary' = ordinary-1
    /\ UNCHANGED <<limit, retired, private, guarded, checked, unlimited, mode, bad>>
ReleaseRetired ==
    /\ retired > 0 /\ retired' = retired-1
    /\ UNCHANGED <<limit, ordinary, private, guarded, checked, unlimited, mode, bad>>
UnlimitedClaim ==
    /\ mode = 0 /\ unlimited' = unlimited+1
    /\ UNCHANGED <<limit, ordinary, retired, private, guarded, checked, mode, bad>>
UnlimitedRelease ==
    /\ mode = 0 /\ unlimited > 0 /\ unlimited' = unlimited-1
    /\ UNCHANGED <<limit, ordinary, retired, private, guarded, checked, mode, bad>>
\* The configuration barrier waits for all allocation transactions. A lower
\* cap packs durable owners first and marks surplus slots retired. This also
\* represents activating a limit and backfilling from durable lease snapshots.
Configure(n) ==
    /\ private = 0 /\ guarded = 0
    /\ LET total == ordinary+retired+unlimited
       IN /\ ordinary' = Min(total,n) /\ retired' = total-Min(total,n)
    /\ limit' = n /\ unlimited' = 0 /\ mode' = 1
    /\ UNCHANGED <<private, guarded, checked, bad>>
RemoveLimit ==
    /\ private = 0 /\ guarded = 0
    /\ unlimited' = ordinary+retired+unlimited /\ ordinary' = 0 /\ retired' = 0 /\ mode' = 0
    /\ UNCHANGED <<limit, private, guarded, checked, bad>>
Next == \/ ReserveOrdinary \/ ReserveOverflow \/ CommitOrdinary \/ CommitOverflow
        \/ AbortOrdinary \/ AbortOverflow \/ ReleaseOrdinary \/ ReleaseRetired
        \/ UnlimitedClaim \/ UnlimitedRelease \/ RemoveLimit
        \/ \E n \in Nat : Configure(n)
Spec == Init /\ [][Next]_vars

THEOREM InitialInvariant == Init => Inv
BY SMT DEF Init, Inv
THEOREM InductiveInvariant == Inv /\ [Next]_vars => Inv'
BY SMT DEF Inv, Next, ReserveOrdinary, ReserveOverflow, CommitOrdinary, CommitOverflow,
  AbortOrdinary, AbortOverflow, ReleaseOrdinary, ReleaseRetired, UnlimitedClaim,
  UnlimitedRelease, Configure, RemoveLimit, Min, vars
THEOREM QuotaSafety == Spec => []Inv
BY InitialInvariant, InductiveInvariant, PTL DEF Spec

\* Atomic multi-tag commit lifts the per-tag admission bound. Partial private
\* reservations do not publish ownership; abort keeps the committed map intact.
THEOREM MultiTagCommit ==
    \A Tags : \A usage, caps \in [Tags -> Nat], wanted \in SUBSET Tags :
      (\A tag \in wanted : usage[tag] < caps[tag]) =>
        LET after == [tag \in Tags |-> IF tag \in wanted THEN usage[tag]+1 ELSE usage[tag]]
        IN /\ \A tag \in wanted : after[tag] <= caps[tag]
           /\ \A tag \in Tags \ wanted : after[tag] = usage[tag]
BY SMT
=============================================================================
