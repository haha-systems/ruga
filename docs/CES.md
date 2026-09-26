# Cumulative Epistemic State (CES)

**Cumulative Epistemic State (CES)** is an architecture for structuring AI reasoning as a sequence of governed transformations over a persistent, explicit belief state.

Instead of giving an LLM an entire problem and asking it to investigate, diagnose, plan, implement, and verify everything inside one conversational context, CES separates cognition into bounded phases:

```text
TRIAGE → ABDUCE → FRAME → EXECUTE → CLOSE
```

Each phase receives the original task plus a projection of the accumulated epistemic state. It performs one specific kind of reasoning, produces a structured artifact, and then stops. A fresh model context is used for the next phase rather than handing over the previous model's transcript.

The important idea is that the shared computational object is **not a conversation**. It is the system's current epistemic state: what it has observed, what it believes, what hypotheses remain plausible, what is still unknown, what constraints apply, what intervention it intends to make, what actions it has taken, and what outcomes have been observed.

A CES state can contain objects such as:

```text
Observations   raw evidence from tools, files, tests, users, etc.
Claims         interpretations of observations
Hypotheses     causal explanations
Unknowns       unresolved questions
Constraints    things an intervention must preserve
Frames         the currently justified intervention/plan
Actions        changes made to the world/code
Outcomes       observed consequences of those actions
Relations      supports, contradicts, resolves, depends_on, tests, etc.
```

Evidence is represented through explicit relations rather than being buried in prose:

```text
Observation O7 ─supports────▶ Hypothesis H2
Observation O9 ─contradicts─▶ Hypothesis H1
Frame F1       ─depends_on──▶ Hypothesis H2
Action A3      ─implements──▶ Frame F1
Outcome R4     ─tests───────▶ Action A3
```

CES is **append-only and revision-aware**. Earlier beliefs are not silently overwritten. Hypotheses can be weakened or rejected, Unknowns can be resolved and later reopened, Frames can be superseded, and contradictory evidence remains visible.

Each phase has limited epistemic authority.

```text
TRIAGE
Locate the problem, establish boundaries, record observations and unknowns.
Do not diagnose or fix it.

ABDUCE
Generate and discriminate between causal hypotheses.
Do not modify production state.

FRAME
Turn the supported diagnosis into a bounded intervention contract.
Define invariants, constraints and success/failure conditions.

EXECUTE
Carry out the active Frame.
If reality contradicts the Frame or diagnosis, record the evidence rather
than improvising a new explanation.

CLOSE
Independently attempt to falsify completion.
Verify the original problem and expose contradictions or residual uncertainty.
```

The orchestration itself is deterministic. Models do not decide which phase happens next.

The normal path is:

```text
TRIAGE → ABDUCE → FRAME → EXECUTE → CLOSE → COMPLETE
```

But contradictions can reopen earlier cognition:

```text
contradicted Hypothesis → ABDUCE
contradicted Frame      → FRAME
contradicted Action     → EXECUTE
contradicted Outcome    → EXECUTE
```

This creates a reasoning process capable of **belief revision** rather than simply continuing forward after a bad assumption.

Another important part of CES is that a phase may reference only information included in its projection. Hidden state cannot be guessed or mutated. Projections are also **counterevidence-closed**: if Ghost is shown a hypothesis, known evidence contradicting that hypothesis cannot quietly be omitted.

CES is separate from resource allocation. In Ghost:

```text
CES / Orchestrator
    decides WHAT kind of cognition must happen next.

QAC
    decides WHO / which model / which cognitive tier should perform it.

Runtime
    executes that cognitive resource.

Ghostdive
    eventually remembers and consolidates completed epistemic trajectories.
```

This separation makes it possible to use inexpensive models for bounded cognitive operations and reserve stronger or more expensive reasoning for places where uncertainty actually warrants it.

A useful way to think about CES is:

> **Rather than asking an agent to solve a problem, maintain a governed belief state and let specialized cognitive operations transform it until the problem is resolved.**

The hypothesis behind CES is that system-level reasoning quality can improve not only by using a stronger model, but by improving the **architecture of cognition around the model**: reducing task breadth, preserving evidence, separating epistemic responsibilities, forcing explicit revision, and allocating compute according to where it is useful.

In short:

```text
ordinary agent:
task → model → answer

CES:
task
 ↓
persistent epistemic state
 ↓
bounded cognitive transformation
 ↓
revised epistemic state
 ↓
evidence / contradiction / action
 ↓
further transformation
 ↓
verified completion
```

The model is the cognitive substrate.

**CES is the structure that tells that substrate what kind of thinking it is allowed to do, what it currently knows, and how new evidence changes what the system believes.**
