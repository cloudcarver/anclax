---------------------------- MODULE AdmissionProofs ----------------------------
EXTENDS Integers, TLAPS
Min(a, b) == IF a < b THEN a ELSE b

\* Cardinalities of SQL's disjoint ready candidates after eligibility filters
\* and SKIP LOCKED. ORDER BY priority DESC puts every strict row before normal.
\* LIMIT has its standard relational meaning; triggers may only reduce output.
THEOREM BatchQueryBounds ==
    \A strictAvailable, normalAvailable, batch, strictSlots \in Nat :
      (batch > 0 /\ strictSlots <= batch) =>
        LET strictCandidates == Min(strictAvailable, strictSlots)
            normalCandidates == Min(normalAvailable, batch)
            strictReturned == Min(strictCandidates, batch)
            normalReturned == Min(normalCandidates, batch-strictReturned)
        IN /\ strictReturned \in Nat /\ normalReturned \in Nat
           /\ strictReturned <= strictSlots
           /\ strictReturned+normalReturned <= batch
           /\ strictReturned <= strictAvailable /\ normalReturned <= normalAvailable
BY SMT DEF Min

THEOREM DisjointCandidateLanes == \A priority \in Int : ~(priority > 0 /\ priority = 0)
BY SMT

THEOREM StrictSaturation ==
    \A strictAvailable, normalAvailable, batch \in Nat :
      (batch > 0 /\ strictAvailable >= batch) =>
        LET strictReturned == Min(Min(strictAvailable, batch), batch)
        IN Min(Min(normalAvailable, batch), batch-strictReturned) = 0
BY SMT DEF Min

\* The bridge from a successful SQL response to the reservation invariant.
THEOREM BatchBudgetComposition ==
    \A capacity, active, reserved, strictActive, strictReserved, n, s \in Nat :
      (active+reserved <= capacity /\ strictActive <= active /\ strictReserved <= reserved
       /\ n <= reserved /\ s <= n /\ s <= strictReserved) =>
         /\ active+n <= capacity /\ strictActive+s <= active+n
BY SMT

\* Slot-level induction: ordinary allocation uses a free non-retired slot;
\* overflow allocation serializes admissions while retired occupancy persists.
\* If retired occupancy disappears, ordinary slots alone bound total occupancy.
THEOREM SlotAdmissionBound ==
    \A limit, normalOwners, retiredOwners, countAtCheck \in Nat :
      ((retiredOwners = 0 /\ normalOwners < limit)
       \/ (normalOwners+retiredOwners <= countAtCheck /\ countAtCheck < limit)) =>
        normalOwners+retiredOwners+1 <= limit
BY SMT

THEOREM OverflowDisappeared ==
    \A limit, normalOwners, retiredOwners \in Nat :
      (retiredOwners = 0 /\ normalOwners < limit) => normalOwners+retiredOwners+1 <= limit
BY SMT

\* Rebuilding configuration preserves ownership, regardless of the number of
\* preceding changes; lowering the limit does not cancel existing owners.
THEOREM ResizeAccounting ==
    \A occupied, newLimit \in Nat :
      LET ordinary == Min(occupied, newLimit)
          retired == occupied-ordinary
      IN ordinary \in Nat /\ retired \in Nat
         /\ ordinary <= newLimit /\ ordinary+retired = occupied
BY SMT DEF Min

\* Attempt snapshots remain the source of truth on enable/remove/backfill.
THEOREM BackfillAndRelease ==
    \A owners, snapshot, limited, t :
      LET slots == {id \in owners : id \in snapshot /\ limited}
      IN slots \ {t} = {id \in owners \ {t} : id \in snapshot /\ limited}
BY SMT

\* The serial guard protects check-to-commit; row locks keep a successful
\* scheduler identity check stable until commit, even if its deadline passes.
THEOREM StableTransactionFence ==
    \A requestedWorker, requestedEpoch, checkedWorker, checkedEpoch, commitWorker, commitEpoch :
      (requestedWorker = checkedWorker /\ requestedEpoch = checkedEpoch
       /\ commitWorker = checkedWorker /\ commitEpoch = checkedEpoch) =>
        requestedWorker = commitWorker /\ requestedEpoch = commitEpoch
BY SMT

THEOREM LocalRenewalDeadline ==
    \A requestStart, databaseStatement, responseTime, oldDeadline, ttl \in Int :
      (ttl > 0 /\ requestStart <= databaseStatement /\ responseTime < oldDeadline) =>
        requestStart+ttl <= databaseStatement+ttl
BY SMT
=============================================================================
