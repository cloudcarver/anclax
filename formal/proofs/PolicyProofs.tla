------------------------------ MODULE PolicyProofs ------------------------------
EXTENDS Integers, TLAPS
Kinds == {"success", "error", "silent", "fatal", "lost", "cancel", "pause", "defer", "interrupt"}
Max(a,b) == IF a > b THEN a ELSE b
Result(status, attempts, writes, completion, failureHook) ==
    [status |-> status, attempts |-> attempts, writes |-> writes,
     completion |-> completion, failureHook |-> failureHook]
Retry(kind, hasPolicy, bound, attempts) ==
    kind \in {"error", "silent"} /\ hasPolicy = 1 /\ (bound < 0 \/ attempts < bound)
\* Validity flags are results of duration/cron parsing. Actual timestamp and
\* calendar calculations belong to the time/cron library's trusted contract.
Decide(kind, attempts, hasPolicy, bound, retryValid, cron, cronValid, delay) ==
    IF kind = "lost" THEN Result("completed", attempts, 0, 0, 0)
    ELSE IF kind = "cancel" THEN Result("cancelled", attempts, 1, 0, 0)
    ELSE IF kind = "pause" THEN Result("paused", attempts, 1, 0, 0)
    ELSE IF kind \in {"defer", "interrupt"} THEN
      Result("pending", Max(0,attempts-1), IF delay >= 0 THEN 1 ELSE 0, 0, 0)
    ELSE IF Retry(kind, hasPolicy, bound, attempts) THEN
      Result("pending", attempts, retryValid, 0, 0)
    ELSE IF cron = 1 THEN
      Result("pending", 0, cronValid, 0, IF kind = "success" THEN 0 ELSE 1)
    ELSE IF kind = "success" THEN Result("completed", attempts, 1, 1, 0)
    ELSE Result("failed", attempts, 1, 0, 1)

THEOREM ControlPrecedesRetryAndCron ==
    \A attempts \in Nat, p, retryValid, cron, cronValid \in 0..1, bound, delay \in Int :
      /\ Decide("cancel",attempts,p,bound,retryValid,cron,cronValid,delay).status = "cancelled"
      /\ Decide("pause",attempts,p,bound,retryValid,cron,cronValid,delay).status = "paused"
      /\ Decide("lost",attempts,p,bound,retryValid,cron,cronValid,delay).writes = 0
BY SMT DEF Decide, Result, Retry, Max

THEOREM DeferralDoesNotSpendAttempt ==
    \A kind \in {"defer","interrupt"}, attempts \in Nat, p, rv, cron, cv \in 0..1, bound, delay \in Int :
      LET out == Decide(kind,attempts,p,bound,rv,cron,cv,delay)
      IN out.attempts \in Nat /\ out.attempts <= attempts /\ out.completion = 0
BY SMT DEF Decide, Result, Retry, Max

THEOREM FiniteRetryBound ==
    \A kind \in {"error","silent","fatal"}, attempts, bound \in Nat, p, rv \in 0..1 :
      attempts >= bound => Decide(kind,attempts,p,bound,rv,0,1,0).status = "failed"
BY SMT DEF Decide, Result, Retry, Max

THEOREM FatalDoesNotRetry ==
    \A attempts \in Nat, bound \in Int, p, rv \in 0..1 :
      Decide("fatal",attempts,p,bound,rv,0,1,0).status = "failed"
BY SMT DEF Decide, Result, Retry, Max

THEOREM CronRestartsAttemptCount ==
    \A kind \in {"success","error","silent","fatal"}, attempts \in Nat, bound \in Int, p, rv \in 0..1 :
      ~Retry(kind,p,bound,attempts) =>
        LET out == Decide(kind,attempts,p,bound,rv,1,1,0)
        IN out.status = "pending" /\ out.attempts = 0 /\ out.completion = 0
BY SMT DEF Decide, Result, Retry, Max

THEOREM CompletionOnlyForSuccessfulOneShot ==
    \A kind \in Kinds, attempts \in Nat, p, rv, cron, cv \in 0..1, bound, delay \in Int :
      Decide(kind,attempts,p,bound,rv,cron,cv,delay).completion = 1 => kind = "success" /\ cron = 0
BY SMT DEF Decide, Result, Retry, Max, Kinds
=============================================================================
