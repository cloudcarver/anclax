------------------------------ MODULE TagSlots ------------------------------
EXTENDS Naturals, FiniteSets

\* Statement-boundary model of migration 15's independent tag slots.
\* Committed slot owners and transaction-private writes are separate.
CONSTANTS TaskCount, TagCount, SlotCount, None, Needs, AllowResize,
          RecheckSlot, RollbackPartial, CheckOverflow, SerializeOverflow,
          ConfigurationBarrier
Tasks == 1..TaskCount
Tags == 1..TagCount
Slots == 1..SlotCount
Places == Tags \X Slots
AllTags == [t \in Tasks |-> Tags]
MultiTagNeeds == [t \in Tasks |-> IF t = 1 THEN Tags ELSE {TagCount}]
Phases == {"idle", "tag", "overflow", "count", "scan", "guard", "recheck"}

VARIABLES owners, active, limits, resized, phase, position, candidate,
          pending, guards, overflowGuard, badAdmission
vars == <<owners, active, limits, resized, phase, position, candidate,
          pending, guards, overflowGuard, badAdmission>>

Init ==
    /\ owners = [p \in Places |-> None]
    /\ active = {}
    /\ limits = [g \in Tags |-> SlotCount]
    /\ resized = FALSE
    /\ phase = [t \in Tasks |-> "idle"]
    /\ position = [t \in Tasks |-> 1]
    /\ candidate = [t \in Tasks |-> 0]
    /\ pending = [t \in Tasks |-> {}]
    /\ guards = [p \in Places |-> None]
    /\ overflowGuard = [g \in Tags |-> None]
    /\ badAdmission = FALSE

Owned(t, g) == {s \in Slots : owners[<<g, s>>] = t}
Usage(g) == Cardinality({s \in Slots : owners[<<g, s>>] # None})
Overflow(g) == \E s \in Slots : s > limits[g] /\ owners[<<g, s>>] # None
Place(t) == <<position[t], candidate[t]>>
DropGuards(t) == [p \in Places |-> IF guards[p] = t THEN None ELSE guards[p]]
DropOverflow(t) == [g \in Tags |-> IF overflowGuard[g] = t THEN None ELSE overflowGuard[g]]
Publish(t) == [p \in Places |-> IF p \in pending[t] THEN t ELSE owners[p]]

Begin(t) ==
    /\ phase[t] = "idle" /\ t \notin active
    /\ phase' = [phase EXCEPT ![t] = "tag"]
    /\ UNCHANGED <<owners, active, limits, resized, position, candidate,
                   pending, guards, overflowGuard, badAdmission>>

SkipTag(t) ==
    /\ phase[t] = "tag" /\ position[t] <= TagCount
    /\ position[t] \notin Needs[t]
    /\ position' = [position EXCEPT ![t] = @ + 1]
    /\ UNCHANGED <<owners, active, limits, resized, phase, candidate,
                   pending, guards, overflowGuard, badAdmission>>

InspectOverflow(t) ==
    /\ phase[t] = "tag" /\ position[t] \in Needs[t]
    /\ phase' = [phase EXCEPT ![t] =
         IF CheckOverflow /\ Overflow(position[t]) THEN "overflow" ELSE "scan"]
    /\ UNCHANGED <<owners, active, limits, resized, position, candidate,
                   pending, guards, overflowGuard, badAdmission>>

TakeOverflowGuard(t) ==
    /\ phase[t] = "overflow"
    /\ (~SerializeOverflow \/ overflowGuard[position[t]] = None)
    /\ overflowGuard' = IF SerializeOverflow
         THEN [overflowGuard EXCEPT ![position[t]] = t] ELSE overflowGuard
    /\ phase' = [phase EXCEPT ![t] = "count"]
    /\ UNCHANGED <<owners, active, limits, resized, position, candidate,
                   pending, guards, badAdmission>>

\* A fresh statement AFTER the per-tag overflow guard, held through commit.
CountOverflow(t) ==
    /\ phase[t] = "count"
    /\ Usage(position[t]) < limits[position[t]]
    /\ phase' = [phase EXCEPT ![t] = "scan"]
    /\ UNCHANGED <<owners, active, limits, resized, position, candidate,
                   pending, guards, overflowGuard, badAdmission>>

\* The cursor's snapshot can be stale by the time the guard is acquired.
Scan(t, s) ==
    /\ phase[t] = "scan" /\ s <= limits[position[t]]
    /\ owners[<<position[t], s>>] = None
    /\ candidate' = [candidate EXCEPT ![t] = s]
    /\ phase' = [phase EXCEPT ![t] = "guard"]
    /\ UNCHANGED <<owners, active, limits, resized, position,
                   pending, guards, overflowGuard, badAdmission>>

TakeSlotGuard(t) ==
    /\ phase[t] = "guard" /\ guards[Place(t)] = None
    /\ guards' = [guards EXCEPT ![Place(t)] = t]
    /\ phase' = [phase EXCEPT ![t] = "recheck"]
    /\ UNCHANGED <<owners, active, limits, resized, position, candidate,
                   pending, overflowGuard, badAdmission>>

\* Conditional UPDATE uses current ownership, not the cursor's free-slot fact.
WriteSlot(t) ==
    /\ phase[t] = "recheck"
    /\ (~RecheckSlot \/ (owners[Place(t)] = None /\ candidate[t] <= limits[position[t]]))
    /\ pending' = [pending EXCEPT ![t] = @ \cup {Place(t)}]
    /\ position' = [position EXCEPT ![t] = @ + 1]
    /\ candidate' = [candidate EXCEPT ![t] = 0]
    /\ phase' = [phase EXCEPT ![t] = "tag"]
    /\ UNCHANGED <<owners, active, limits, resized, guards, overflowGuard, badAdmission>>

\* A busy guard, full quota, changed slot or failed transaction can abort the
\* candidate. Retrying another slot/candidate is abstracted as Begin again.
Abort(t) ==
    /\ phase[t] # "idle"
    /\ owners' = IF RollbackPartial THEN owners ELSE Publish(t)
    /\ phase' = [phase EXCEPT ![t] = "idle"]
    /\ position' = [position EXCEPT ![t] = 1]
    /\ candidate' = [candidate EXCEPT ![t] = 0]
    /\ pending' = [pending EXCEPT ![t] = {}]
    /\ guards' = DropGuards(t) /\ overflowGuard' = DropOverflow(t)
    /\ UNCHANGED <<active, limits, resized, badAdmission>>

Commit(t) ==
    /\ phase[t] = "tag" /\ position[t] > TagCount
    /\ owners' = Publish(t) /\ active' = active \cup {t}
    /\ badAdmission' = (badAdmission \/ \E g \in Needs[t] : Usage(g) >= limits[g])
    /\ phase' = [phase EXCEPT ![t] = "idle"]
    /\ position' = [position EXCEPT ![t] = 1]
    /\ candidate' = [candidate EXCEPT ![t] = 0]
    /\ pending' = [pending EXCEPT ![t] = {}]
    /\ guards' = DropGuards(t) /\ overflowGuard' = DropOverflow(t)
    /\ UNCHANGED <<limits, resized>>

\* Release holds its task row, never the allocation or overflow advisory locks.
\* Its authorization is supplied by the separate TaskLease protocol.
Release(t) ==
    /\ phase[t] = "idle" /\ t \in active
    /\ owners' = [p \in Places |-> IF owners[p] = t THEN None ELSE owners[p]]
    /\ active' = active \ {t}
    /\ UNCHANGED <<limits, resized, phase, position, candidate,
                   pending, guards, overflowGuard, badAdmission>>

OwnerSet(g) == {t \in Tasks : Owned(t, g) # {}}
Rank(t, g) == Cardinality({other \in OwnerSet(g) : other <= t})
Packed(g, s) == IF s <= Cardinality(OwnerSet(g))
               THEN CHOOSE t \in OwnerSet(g) : Rank(t, g) = s
               ELSE None

\* One configuration shrink, at any point, with durable owners packed first.
\* Re-enabling unlimited tags and metadata backfill are not modeled here.
Resize(g, n) ==
    /\ AllowResize /\ ~resized /\ n < limits[g]
    /\ (~ConfigurationBarrier \/ \A t \in Tasks : phase[t] = "idle")
    /\ owners' = [p \in Places |-> IF p[1] = g THEN Packed(g, p[2]) ELSE owners[p]]
    /\ limits' = [limits EXCEPT ![g] = n] /\ resized' = TRUE
    /\ UNCHANGED <<active, phase, position, candidate, pending,
                   guards, overflowGuard, badAdmission>>

Next ==
    \/ \E t \in Tasks :
         \/ Begin(t) \/ SkipTag(t) \/ InspectOverflow(t) \/ TakeOverflowGuard(t)
         \/ CountOverflow(t) \/ TakeSlotGuard(t) \/ WriteSlot(t)
         \/ Abort(t) \/ Commit(t) \/ Release(t)
         \/ \E s \in Slots : Scan(t, s)
    \/ \E g \in Tags, n \in 0..SlotCount : Resize(g, n)
Spec == Init /\ [][Next]_vars

TypeOK ==
    /\ owners \in [Places -> Tasks \cup {None}] /\ active \subseteq Tasks
    /\ limits \in [Tags -> 0..SlotCount] /\ resized \in BOOLEAN
    /\ phase \in [Tasks -> Phases] /\ position \in [Tasks -> 1..(TagCount+1)]
    /\ candidate \in [Tasks -> 0..SlotCount] /\ pending \in [Tasks -> SUBSET Places]
    /\ guards \in [Places -> Tasks \cup {None}]
    /\ overflowGuard \in [Tags -> Tasks \cup {None}] /\ badAdmission \in BOOLEAN
AllOrNothing ==
    \A t \in Tasks, g \in Tags :
      Cardinality(Owned(t, g)) = IF t \in active /\ g \in Needs[t] THEN 1 ELSE 0
PrivateWritesGuarded ==
    \A t \in Tasks :
      /\ \A p \in pending[t] : guards[p] = t
      /\ phase[t] = "idle" =>
           /\ pending[t] = {} /\ \A p \in Places : guards[p] # t
           /\ \A g \in Tags : overflowGuard[g] # t
NoNewOversubscription == ~badAdmission
NoOverflow == ~\E g \in Tags : Usage(g) > limits[g]
NoPartialAllocation == ~\E t \in Tasks : pending[t] # {} /\ position[t] <= TagCount
NoStaleCursor == ~\E t \in Tasks : phase[t] = "guard" /\ owners[Place(t)] # None
=============================================================================
