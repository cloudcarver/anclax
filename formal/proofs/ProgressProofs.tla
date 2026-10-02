---------------------------- MODULE ProgressProofs ----------------------------
EXTENDS Integers, NaturalsInduction, TLAPS
VARIABLE remaining
vars == <<remaining>>
TypeOK == remaining \in Nat
Decrease == remaining > 0 /\ remaining' \in 0..(remaining-1)
\* Environment contract: after stop, no new work; whenever work remains, some
\* outstanding claim/execute/finalize eventually returns and reduces its rank.
Behavior == []TypeOK /\ [][Decrease]_vars /\ WF_vars(Decrease)

LEMMA EnabledDecrease ==
    ASSUME TypeOK
    PROVE (ENABLED <<Decrease>>_vars) <=> remaining > 0
BY ExpandENABLED, SMT DEF Decrease, vars, TypeOK

THEOREM EventualDrain == Behavior => <>(remaining = 0)
<1> DEFINE P(n) == [](remaining <= n => <>(remaining = 0))
<1>1. Behavior => P(0)
  <2>1. TypeOK /\ remaining <= 0 => remaining = 0
    BY SMT DEF TypeOK
  <2> QED BY <2>1, PTL DEF Behavior, P
<1>2. ASSUME NEW n \in Nat
      PROVE (Behavior => P(n)) => (Behavior => P(n+1))
  <2>1. remaining = n+1 /\ [Decrease]_vars =>
           (remaining = n+1)' \/ (remaining <= n)'
    BY SMT DEF Decrease, vars
  <2>2. remaining = n+1 /\ <<Decrease>>_vars => (remaining <= n)'
    BY SMT DEF Decrease, vars
  <2>3. TypeOK /\ remaining = n+1 => ENABLED <<Decrease>>_vars
    BY EnabledDecrease, SMT
  <2>4. Behavior => [](remaining = n+1 => <>(remaining <= n))
    BY <2>1, <2>2, <2>3, PTL DEF Behavior
  <2>5. TypeOK /\ remaining <= n+1 => remaining <= n \/ remaining = n+1
    BY SMT DEF TypeOK
  <2> QED BY <2>4, <2>5, PTL DEF Behavior, P
<1>3. Behavior => \A n \in Nat : P(n)
  <2> HIDE DEF Behavior, P
  <2> QED BY <1>1, <1>2, NatInduction
<1>4. ASSUME NEW n \in Nat PROVE Behavior => (remaining = n => <>(remaining = 0))
  <2>1. Behavior => P(n) BY <1>3
  <2>2. remaining = n => remaining <= n BY SMT
  <2> QED BY <2>1, <2>2, PTL DEF P
<1>5. Behavior => \E n \in Nat : remaining = n
  <2>1. TypeOK => \E n \in Nat : remaining = n BY SMT DEF TypeOK
  <2> QED BY <2>1, PTL DEF Behavior
<1> QED BY <1>4, <1>5

\* A pending claim has weight 3, execution 2, finalization 1. Batch replies
\* replace r claim reservations by n<=r executions; all completed commands
\* strictly decrease this rank after stop. Fallback/retries can stutter.
THEOREM BatchResponseDecreasesRank ==
    \A r, n, executing, finalizing \in Nat :
      (r > 0 /\ n <= r) =>
        2*(executing+n)+finalizing < 3*r+2*executing+finalizing
BY SMT
THEOREM ExecutionDecreasesRank ==
    \A claiming, executing, finalizing \in Nat : executing > 0 =>
      3*claiming+2*(executing-1)+(finalizing+1) < 3*claiming+2*executing+finalizing
BY SMT
THEOREM FinalizationDecreasesRank ==
    \A claiming, executing, finalizing \in Nat : finalizing > 0 =>
      3*claiming+2*executing+(finalizing-1) < 3*claiming+2*executing+finalizing
BY SMT
=============================================================================
