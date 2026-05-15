# Vested Pillars — Community Discussion

## The Problem

Today, contributors who build on and add significant value to the network have limited paths to gaining a meaningful stake in it. The Accelerator program is one option, but it's focused on funding specific projects — it's not designed to reward ongoing contribution with network ownership.

There's no direct path for a builder to demonstrate their value to the network and be granted a significant stake as a result. The only way in is to buy both ZNN and QSR at market price, which may not reflect the contribution they've already made or plan to make.

## The Proposal

A governance-gated path for contributors to earn a pillar: **Vested Pillars**.

Instead of purchasing QSR, a builder submits a proposal to the existing pillar network explaining who they are, what they've built, and what they plan to contribute. The network votes on whether that contribution merits a pillar. If approved, the contributor gets a pillar at reduced cost — just 15,000 ZNN instead of 15,000 ZNN plus the full QSR burn.

## How It Works

The process has three stages:

### 1. Apply

An applicant sends **15,000 ZNN** to the Pillar contract along with:

- **Title** — name of the proposal
- **Description** — why this operator should be approved
- **URL** — link to supporting information (website, socials, track record)

The 15,000 ZNN is held by the contract. It serves dual purpose: it demonstrates commitment and it converts directly into the pillar stake if approved.

### 2. Governance Vote

Once submitted, the application enters a **14-day voting period**. During this time, active pillars can vote **Yes** or **No** on the application using their producing address.

**Approval requires:**
- More Yes votes than No votes
- At least 50% of active pillars participating in the vote

### 3. Outcome

**If approved:**
- The applicant has **30 days** to call `RegisterVested` and finalise their pillar
- The held 15,000 ZNN converts directly into the pillar stake — no additional ZNN or QSR needed
- The pillar is registered with type `VestedPillar` (distinguishable from normal pillars)

**If rejected (votes don't meet threshold):**
- The 15,000 ZNN is automatically refunded to the applicant

**If expired (approved but not registered within 30 days):**
- The 15,000 ZNN is automatically refunded to the applicant

**If voting period expires without meeting the threshold:**
- Treated as a rejection — 15,000 ZNN refunded

## Key Properties

### Reduced Cost for Contributors
Vested pillars bypass the QSR burn entirely. This recognises that builders who contribute to the network bring value beyond capital — their work strengthens the ecosystem and that contribution offsets the QSR cost.

### Vested Pillars Don't Affect QSR Cost
Vested pillars are tracked separately from normal pillars. The `GetQsrCostForNextPillar` calculation (which determines the QSR burn for standard registration) only counts normal pillars. Registering a vested pillar does not increase the QSR cost for everyone else.

### One Application Per Address
An address can only have one open application at a time (either in voting or approved status). This prevents spam.

### Full Refunds
No ZNN is ever lost. If the application is rejected, expires, or the applicant never registers after approval, the full 15,000 ZNN is returned.

### Governance Controlled by Pillars
The decision rests with the people who have the most skin in the game — existing pillar operators. They are incentivised to approve legitimate operators (growing the network) and reject bad actors (protecting network quality).

## Proposed Parameters

| Parameter | Value | Notes |
|-----------|-------|-------|
| Application Fee | 15,000 ZNN | Same as normal pillar stake — converts on register |
| Voting Period | 14 days | Time for pillars to review and vote |
| Approval Threshold | 50% participation | Stricter than the Accelerator's 33% — discussion welcome |
| Approval Grace Period | 30 days | Time to register after approval |

> **Note:** The 50% threshold is a starting suggestion. This is a governance decision and should be discussed before the parameters are finalised.

## Technical Summary

- New pillar type: `VestedPillarType = 3` (alongside existing `Legacy = 1`, `Normal = 2`)
- Voting reuses the existing pillar voting infrastructure (`VoteByName`, `VoteByProdAddress`)
- Application state machine: `Voting → Approved/Rejected`; `Approved → Registered/Expired`
- All new code lives in the pillar contract (`pillars.go`) — no changes to the accelerator or other contracts
- Fully backward compatible — existing pillar registration and functionality unchanged

## Questions for Discussion

1. **Is 50% the right approval threshold?** The Accelerator uses 33%. A higher threshold means broader consensus but makes it harder to reach approval. What balance feels right?

2. **Is 14 days enough voting time?** Pillars need time to evaluate applications and assess the applicant's contribution. Is two weeks sufficient, or should it be longer?

3. **What qualifies as a meaningful contribution?** Should applicants need to demonstrate completed work, or is a credible plan enough? How do pillars evaluate this?

4. **Should vested pillars have any restrictions compared to normal pillars?** (e.g., lower delegation weight cap, different reward structure) Or should they be fully equivalent once granted?

5. **Spork gating — phased rollout?** Should this ship behind a spork for staged activation, or is it safe to activate immediately?
