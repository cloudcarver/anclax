----------------------------- MODULE TaskLease -----------------------------
EXTENDS Naturals, FiniteSets

\* A committed-state abstraction of the task-row lease protocol, NOT of the
\* shared tag allocator. Each task has one abstract resource reservation.
\* See README.md for SQL mappings, bounds, and unproved refinement assumptions.
CONSTANTS Tasks, Workers, MaxVersion, None,
          FenceFinalize, PreserveControl, ReleaseOnlyOwn

Statuses == {"pending", "ready", "running", "paused", "cancelled",
             "completed", "failed"}
Outcomes == Statuses \ {"ready", "running"}
ControlStates == {"paused", "cancelled"}
Leases == {None, "valid", "expired"}
Attempt == [task : Tasks, worker : Workers, version : 1..MaxVersion]
Row == [status : Statuses, owner : Workers \cup {None},
        version : 0..MaxVersion, lease : Leases]
Transaction == [attempt : Attempt, outcome : Outcomes]

ASSUME /\ IsFiniteSet(Tasks) /\ Tasks # {}
       /\ IsFiniteSet(Workers) /\ Workers # {} /\ None \notin Workers
       /\ MaxVersion \in Nat \ {0}
       /\ FenceFinalize \in BOOLEAN
       /\ PreserveControl \in BOOLEAN
       /\ ReleaseOnlyOwn \in BOOLEAN

VARIABLES rows, permits, issued, finalizing,
          staleFinalization, overwrittenControl
vars == <<rows, permits, issued, finalizing,
          staleFinalization, overwrittenControl>>

Init ==
    /\ rows = [t \in Tasks |->
         [status |-> "pending", owner |-> None, version |-> 0, lease |-> None]]
    /\ permits = [t \in Tasks |-> 0]
    /\ issued = {}
    /\ finalizing = [t \in Tasks |-> None]
    /\ staleFinalization = FALSE
    /\ overwrittenControl = FALSE

Unlocked(t) == finalizing[t] = None
Available(t) == rows[t].lease \in {None, "expired"}
Matches(a) == /\ rows[a.task].owner = a.worker
              /\ rows[a.task].version = a.version
Normalized(status) == IF status = "running" THEN "pending" ELSE status

\* The mutation changes only resource-release scoping, not task-row updates.
Released(t) == IF ReleaseOnlyOwn THEN [permits EXCEPT ![t] = 0]
              ELSE [other \in Tasks |-> 0]

\* Successful admission is an assumption at this layer. Failed admission is
\* a stuttering step; slot contention, multi-tag rollback and quotas are outside
\* this model. The task row lock serializes admission with its lease operations.
Prepare(t) ==
    /\ Unlocked(t) /\ rows[t].status = "pending" /\ Available(t)
    /\ rows[t].version < MaxVersion
    /\ rows' = [rows EXCEPT ![t] =
         [status |-> "ready", owner |-> None,
          version |-> @.version + 1, lease |-> None]]
    /\ permits' = [permits EXCEPT ![t] = rows[t].version + 1]
    /\ UNCHANGED <<issued, finalizing, staleFinalization, overwrittenControl>>

\* Ready -> running adopts the reservation/version instead of allocating again.
ClaimReady(t, w) ==
    /\ Unlocked(t) /\ rows[t].status = "ready"
    /\ rows' = [rows EXCEPT ![t].status = "running",
                          ![t].owner = w, ![t].lease = "valid"]
    /\ issued' = issued \cup
         {[task |-> t, worker |-> w, version |-> rows[t].version]}
    /\ UNCHANGED <<permits, finalizing, staleFinalization, overwrittenControl>>

\* Legacy/manual pending claims keep status=pending, even during execution.
\* Workers may be reused: a UUID is not an attempt identifier.
ClaimPending(t, w) ==
    /\ Unlocked(t) /\ rows[t].status = "pending" /\ Available(t)
    /\ rows[t].version < MaxVersion
    /\ rows' = [rows EXCEPT ![t].owner = w, ![t].lease = "valid",
                          ![t].version = @ + 1]
    /\ permits' = [permits EXCEPT ![t] = rows[t].version + 1]
    /\ issued' = issued \cup
         {[task |-> t, worker |-> w, version |-> rows[t].version + 1]}
    /\ UNCHANGED <<finalizing, staleFinalization, overwrittenControl>>

\* Relative expiry abstracts DB time; it can occur while a row lock is held.
\* No fairness assumption forces expiry, recovery, or successful execution.
Expire(t) ==
    /\ rows[t].lease = "valid"
    /\ rows' = [rows EXCEPT ![t].lease = "expired"]
    /\ UNCHANGED <<permits, issued, finalizing,
                   staleFinalization, overwrittenControl>>

\* Extending an already-valid deadline is a stutter in this time abstraction.
\* Timing of renewal statements and executor cancellation is not verified here.
Renew(a) ==
    /\ Unlocked(a.task) /\ Matches(a)
    /\ rows[a.task].status \in {"pending", "running"}
    /\ rows[a.task].lease = "valid"
    /\ UNCHANGED vars

Recover(t) ==
    /\ Unlocked(t) /\ rows[t].lease = "expired"
    /\ rows' = [rows EXCEPT ![t].status = Normalized(@),
                          ![t].owner = None, ![t].lease = None]
    /\ permits' = Released(t)
    /\ UNCHANGED <<issued, finalizing, staleFinalization, overwrittenControl>>

Release(a) ==
    /\ Unlocked(a.task) /\ Matches(a)
    /\ rows' = [rows EXCEPT ![a.task].status = Normalized(@),
                          ![a.task].owner = None, ![a.task].lease = None]
    /\ permits' = Released(a.task)
    /\ UNCHANGED <<issued, finalizing, staleFinalization, overwrittenControl>>

Control(t, target) ==
    /\ Unlocked(t)
    /\ \/ rows[t].status \in {"pending", "ready", "running", "paused"}
       \/ /\ rows[t].status = "cancelled" /\ target = "cancelled"
    /\ rows[t].status = "ready" => rows[t].version < MaxVersion
    /\ rows' = [rows EXCEPT ![t].status = target,
         ![t].version = IF rows[t].status = "ready" THEN @ + 1 ELSE @]
    /\ permits' = IF rows[t].status = "ready" THEN Released(t) ELSE permits
    /\ UNCHANGED <<issued, finalizing, staleFinalization, overwrittenControl>>

\* Resume fences the previous attempt but retains its lease and tag snapshot.
\* Thus an outstanding permit version can be less than the row fence version.
Resume(t) ==
    /\ Unlocked(t) /\ rows[t].status = "paused"
    /\ rows[t].version < MaxVersion
    /\ rows' = [rows EXCEPT ![t].status = "pending", ![t].version = @ + 1]
    /\ UNCHANGED <<permits, issued, finalizing,
                   staleFinalization, overwrittenControl>>

EditReady(t) ==
    /\ Unlocked(t) /\ rows[t].status = "ready"
    /\ rows[t].version < MaxVersion
    /\ rows' = [rows EXCEPT ![t].status = "pending", ![t].version = @ + 1]
    /\ permits' = Released(t)
    /\ UNCHANGED <<issued, finalizing, staleFinalization, overwrittenControl>>

\* Represents the successful conditional UPDATE after acquiring its row lock.
\* Dirty task/trigger writes stay private until CommitFinalize; rollback drops
\* them together. Every other modeled DB writer observes the same row lock.
\* No expiry predicate: FinalizeTaskAttempt checks owner, version and status.
BeginFinalize(a, outcome) ==
    /\ Unlocked(a.task)
    /\ rows[a.task].owner = a.worker
    /\ (~FenceFinalize \/ rows[a.task].version = a.version)
    /\ rows[a.task].status \in
         {"pending", "ready", "running", "paused", "cancelled"}
    /\ finalizing' = [finalizing EXCEPT ![a.task] =
         [attempt |-> a, outcome |-> outcome]]
    /\ UNCHANGED <<rows, permits, issued, staleFinalization, overwrittenControl>>

CommitFinalize(t) ==
    /\ finalizing[t] # None
    /\ LET a == finalizing[t].attempt
           oldStatus == rows[t].status
           newStatus == IF PreserveControl /\ oldStatus \in ControlStates
                        THEN oldStatus ELSE finalizing[t].outcome
       IN /\ rows' = [rows EXCEPT ![t].status = newStatus,
                                 ![t].owner = None, ![t].lease = None]
          /\ permits' = Released(t)
          \* Independent history monitors: observe an accepted write, rather
          \* than assuming the desired property in the transition guard.
          /\ staleFinalization' = (staleFinalization \/ ~Matches(a))
          /\ overwrittenControl' = (overwrittenControl \/
               (oldStatus \in ControlStates /\ newStatus # oldStatus))
    /\ finalizing' = [finalizing EXCEPT ![t] = None]
    /\ UNCHANGED issued

RollbackFinalize(t) ==
    /\ finalizing[t] # None
    /\ finalizing' = [finalizing EXCEPT ![t] = None]
    /\ UNCHANGED <<rows, permits, issued, staleFinalization, overwrittenControl>>

Next ==
    \/ \E t \in Tasks :
         \/ Prepare(t) \/ Expire(t) \/ Recover(t) \/ Resume(t) \/ EditReady(t)
         \/ CommitFinalize(t) \/ RollbackFinalize(t)
         \/ \E w \in Workers : ClaimReady(t, w) \/ ClaimPending(t, w)
         \/ \E target \in ControlStates : Control(t, target)
    \/ \E a \in issued :
         \/ Renew(a) \/ Release(a)
         \/ \E outcome \in Outcomes : BeginFinalize(a, outcome)

Spec == Init /\ [][Next]_vars

TypeOK ==
    /\ rows \in [Tasks -> Row]
    /\ permits \in [Tasks -> 0..MaxVersion]
    /\ issued \subseteq Attempt
    /\ finalizing \in [Tasks -> Transaction \cup {None}]
    /\ staleFinalization \in BOOLEAN /\ overwrittenControl \in BOOLEAN

NoStaleFinalize == ~staleFinalization
ControlWins == ~overwrittenControl

ResourcesAccounted ==
    \A t \in Tasks :
      /\ (permits[t] > 0) <=>
           (rows[t].owner \in Workers \/ rows[t].status = "ready")
      /\ permits[t] <= rows[t].version

LeaseShape ==
    \A t \in Tasks :
      /\ (rows[t].owner = None) <=> (rows[t].lease = None)
      /\ rows[t].status = "ready" =>
           /\ rows[t].owner = None /\ permits[t] = rows[t].version
      /\ rows[t].status = "running" => rows[t].owner \in Workers
      /\ rows[t].status \in {"completed", "failed"} => rows[t].owner = None

IssuedVersions ==
    /\ \A a \in issued : a.version <= rows[a.task].version
    /\ \A t \in Tasks : rows[t].owner \in Workers =>
         \E a \in issued : /\ a.task = t /\ a.worker = rows[t].owner
                            /\ a.version = permits[t]

\* Deliberately false invariants used ONLY in witness configurations. Finding
\* these counterexamples proves key scenarios are reachable, not safe.
NoSameWorkerTakeover ==
    ~\E a \in issued : /\ rows[a.task].owner = a.worker
                        /\ permits[a.task] > a.version
NoReadyExecution == ~\E t \in Tasks : rows[t].status = "running"
NoRetainedLeaseAfterResume ==
    ~\E t \in Tasks : /\ rows[t].owner \in Workers
                       /\ rows[t].version > permits[t]
=============================================================================
