# Bothy v0.2 UI redesign plan

Status: accepted plan, 2026-10-14. Implements `docs/design/v0.2.md` §7 (F7) and gives the UI for F1 (Bedrock), F3 (Refine), F4 (GitHub), F5 (releases) and F6 (Knowledge). It replaces `docs/SPEC.md` §9 screen by screen as the chunks in §18 land. Where this file and SPEC §9 disagree, this file wins, and SPEC §9 is updated when the work merges.

Inputs: the v0.1 UI audit (377 screenshots of the built SPA at 1280 and 360 px, light and dark) and two competing proposals, "Quiet tool" (A) and "Friendly workspace" (B). This document is the single plan. The proposals are superseded and nothing in them applies unless it is repeated here.

The owner's feedback this answers: *"The onboarding felt cumbersome and the UI felt rough. The UI felt too 'AI generated'. Follow cleaner principles; it should be clean for the user; the user doesn't need that much technical info, they just need to easily use the product."*

A second round answered *"The UI still feels rough. The login with OAuth doesn't have icons. Make sure the UI/UX is modern, appealing, easy to use and intuitive."* Three visual directions were drawn; the owner chose the **Friendly assistant** one (D46). Its look (a warm canvas around a near-white panel, outline nav icons, Bothy's picture in chat, tone cards, ember for "needs you", brand marks on sign-in) is folded into the rules below, minus what broke them: stacked status signals, filled icon pairs, a streaming caret, a web font and copy for flows Bothy doesn't have (§2.1).

How to read it: §1 records every decision taken between the two proposals. §2 to §4 are the rules (principles, tokens, navigation). §5 to §13 specify every screen and state. §14 is the copy guide, §15 the components, §16 accessibility. §17 lists the server work the screens need, each with a fallback. §18 is the implementation checklist: independent chunks with disjoint files and acceptance criteria. Every value here is meant to be used as written. Wireframes are drawn at 1280 × 800 unless marked.

Notation in wireframes: `[ Label ]` button, `( )` radio, `[x]` checkbox, `(on)` switch, `⌄` select or menu, `›` disclosure or navigation, `⋯` overflow menu, `◌` spinner.

---

## 1. Decisions

The base is proposal A ("Quiet tool"): its subtraction rules, single content column, phrase table, server-need list and testable acceptance checks. Proposal B ("Friendly workspace") supplies the warm palette with the green accent, the verb buttons on approval cards, the disabled-with-a-reason pattern, the model-check flow ("Connected. Bothy will use …  Change"), the explicit "Allow this address" fix for private addresses, step-group summaries, no pill badges at all, settings rows with dividers, touch sizing by pointer type and the copy test. Each contradiction is resolved below.

| # | Topic | A said | B said | Decision | Why |
|---|---|---|---|---|---|
| D1 | Accent colour | Blue `#3553C7`, ink primary buttons | Keep moss green, green primary buttons | **Moss green** (`#2A6A54`), green primary buttons, deep green fill with white text in dark mode | Black buttons plus a blue accent is the default look of generated UI kits. Green is Bothy's colour and the audit said to keep it. The real faults were the mint dark buttons and the green "ok" pills, both fixed below. |
| D2 | Success colour | Separate green, used quietly | Separate green, text only | **No success hue.** A success state is a check glyph in `accent-text` next to ink text. Diffs get their own `diff-add` tint | Two greens 1.1:1 apart was the v0.1 collision. One green, used only for "actionable" and "done", cannot collide. |
| D3 | Neutrals | Near-pure grey | Warm grey | **Warm grey:** an oat canvas (`#F4F0E8`) holds the sidebar; the page sits on a near-white panel (`#FFFDFA`) inset 8 px with 14 px corners at ≥ 1024 px; cards are white | Continuity with v0.1's warmth; the inset panel sets the page apart from the navigation without a border between them (D46). |
| D4 | Radii | 4 / 6 / 8 | 4 / 6 / 8 / 12 | **6 / 10 / 14**, and 20 for the sign-in card only (was 4 / 6 / 8) | The Friendly look's softer corners on a strict three-step scale; one larger radius for the one card that floats on the canvas. `rounded-xl` anywhere else fails lint. |
| D5 | Content width | One 768 px column for every page | 960 px page container, 640 px forms, 720 px chat | **One 768 px column (720 px content) for every page and the chat** | One left edge on every route: x = 408 at 1280 px. Lists drop columns instead of widening. |
| D6 | Control height | 32 px, 40 px under 640 px | 36 px, 44 px on touch | **36 px default, 32 px small, 44 px on `pointer: coarse`** | Touch size follows the input device, not the width. 36 px reads calmer than 32. |
| D7 | Type names | `text-sm`, `text-md`… | `text-body`, `text-reading`… | **Semantic names** (caption, small, body, reading, heading, title, display, code) with Tailwind's size scale reset | Names say where a size is used. Default sizes stop compiling. |
| D8 | Personal settings | Separate "Account" page | "You" section inside General | **Separate Account page** at `/settings/account`, reached from the account menu. The six workspace groups stay as v0.2 lists them | Personal (password, theme, linked apps, API tokens) and workspace settings are different jobs. Members see only Account. |
| D9 | Approval buttons | Approve / Approve for this chat / Deny | Verb ("Delete file") / Allow for this chat / Don't allow | **Verb primary** ("Delete file", "Open pull request", "Approve deploy"), "Allow for this chat", "Deny" | The button names the result. "Deny" stays short and matches "approved/denied" in records and in "1 of 2 approvals". |
| D10 | Destructive approve | Deny is never red | Destructive verb is red | **Both:** Deny is never red; the primary verb is a red fill when the action can't be undone or spends money (deploys excepted, they use the normal fill) | Red marks the dangerous click, never the safe one. |
| D11 | Tool activity | Lines; finished steps fold into "› 4 steps" | Step group with a summary line | **Activity lines folded into a summary line** ("Read 3 files and searched the web"), no glyph on done steps. Reaffirmed for the Friendly look, whose mockups checked every line | Summary says more than a count. Green checks on every step would be the "ok" pills again. |
| D12 | Waiting phrase | "Waiting for approval to delete …" | "Wants to delete …" | **"Waiting for approval to delete …"** | States the state, not an intent. |
| D13 | Model step save | Continue saves | Check success saves | **Check is automatic, Continue saves** | One commit per intent; trying two providers writes nothing extra. |
| D14 | Model choice in setup | Inline Model select | "Connected. Bothy will use X. Change" | **B's line**, select behind "Change" | One decision fewer. |
| D15 | Private addresses | Auto-allow on Continue | Fail with an "Allow host:port" button | **Detected local servers are allowed automatically; a typed private address fails with "Allow {host:port}"** | Opening a private address is a deliberate act unless Bothy found the server itself. |
| D16 | Disabled Continue | Never disabled; error on click | Disabled with a visible reason | **`aria-disabled` with the reason always visible next to it** ("Connect a model to continue."); clicking moves focus to the choices | No mystery, still focusable. |
| D17 | Owner's name | Name + username + password | Username + password | **Name + username + password** | Every approval and record shows names ("Waiting for Alex Kim"); the owner needs one. |
| D18 | Password meter | Removed | Bar fixed to field width | **No bar; live help text** ("4 more characters needed.") | The words carry the meaning; the bar was decoration. |
| D19 | Team mode | Turns on with the first added person | On at setup | **On at setup, silently** (`mode: multi`) | It is a company product. |
| D20 | Setup code screen | Counted as step 1 | Not a step | **Not a step.** A gate with the field first and the command visible below it | Most people who land there need the command. |
| D21 | Invites | Invite links | Admin sets a one-time password | **One-time password, changed at first sign-in** in v0.2; invite links deferred (§19) | Smaller server change, same number of clicks for the admin. |
| D22 | Approvals inbox | One column, expand in place | List and detail side by side | **One column, expand in place**, tabs "Waiting" and "Done", sections "For you" and "Waiting for others" | Same layout at 1280 and 360; respects D5. |
| D23 | Badges | 5 allowed badges | No pills at all | **No badges.** Status is a word, coloured only when abnormal. The Approvals count is a 6 px ember dot and the number in `warning-text`; a dot standing alone is 8 px ember with hidden words; every other count is a plain muted number | Easiest rule to enforce. The one count that asks for action gets the needs-you colour, still without a pill. |
| D24 | Banners | Includes 80 % spend and failing sandbox | Only when work stops | **Only when work stops** | 80 % lives on Spending; sandbox problems live in Security with the attention dot. |
| D25 | Chat list | "Recent" + "All chats" page | Grouped list with "Show more" | **Grouped list (Today, Previous 7 days, Older), "Show more"**, no `/chats` page; related chats fold into one row (D42) | One place for chats. |
| D26 | Empty chat | Composer in the middle; suggestion fills | Composer at the bottom; suggestion sends | **Composer at the bottom; a starter fills, never sends** (the rows come from the server, D44) | The composer never moves; the person can edit before sending. |
| D27 | Chat details, pins | Drawer | Dialog | **Drawer** (400 px, full screen on phones) | The chat stays visible beside it. |
| D28 | Fork clean | "Start a clean copy" | "Start a fresh chat" | **"New related chat"** (it was "Continue in a clean chat", a second menu item making the same chat) | A fresh start without the outside content: the new chat carries none of the messages, private information or outside content, and is listed under the chat it came from (D42). Nothing is lost, so it needs no dialog. |
| D29 | Memory | Two document pages | One page, two sections | **One page, two sections** ("About you", "Things Bothy remembers") | One place. |
| D30 | Heartbeat | Pinned "Check-in" schedule | A checkbox on any schedule | **Pinned "Check-in" row** in Schedules with its own form | Matches the backend: one heartbeat with one checklist per person. |
| D31 | Note suggestions | Routed to Approvals | Own review dialog | **Both surfaces show the same approval card**: a notice on the project page opens a dialog of those cards; the inbox lists them too | One approval system. |
| D32 | Activity naming | "Recent", generated titles | "Tasks", chat or schedule titles | **"Tasks"**, titled by chat or schedule name, outcome on line 2 | No server summariser needed; the noun "task" replaces "run" everywhere. |
| D33 | Toasts | Inverted | Surface with border | **Inverted** (`bg-text text-bg`), bottom left, never over the composer | Reads at a glance without another bordered box. |
| D34 | Empty states | Title + sentence + action | Sentence + action | **Sentence (+ optional muted sentence) + one action**, left-aligned | Shortest form. |
| D35 | Streaming indicator | "Working…" until first output, no caret | "Thinking…", blinking caret | **"Working…" with a spinner until first output, then nothing**; Stop shows Bothy is busy. Reaffirmed: no caret and no dot after streaming text | No motion on streaming text. |
| D36 | Dark-mode CSS | Duplicate tokens under a media query | `data-theme` only | **`data-theme` only** | `theme.js` always resolves "system" before first paint; the SPA cannot run without scripts anyway. |
| D37 | Model slot names | Main / Quick tasks | Everyday / Quick tasks | **"Main model" and "Quick tasks model"**, "Backup models" | Plain and short. |
| D38 | Refine settings | Switch; numbers in Advanced | Switch + "How thorough" | **Switch + "How thorough" (Standard, Thorough)**; exact numbers in Advanced | A plain choice for the paper's heavier setting. |
| D39 | Sidebar collapse | — | Collapsible | **Not in v0.2** (§19) | Not needed to ship. |
| D40 | Approvals on phones | Drawer only | Bell with count | **A "2 waiting" text button in the phone top bar** when something waits for you, plus a dot on the menu button | One tap, no bell icon. |
| D41 | Default approval preset | Careful | Balanced | **Unchanged config default (Careful)** | A UI redesign does not loosen a security default. |
| D42 | Related chats | One row per chat | Virtual group rows | **A chat continued from another nests under it, one level deep; a schedule's results group under its newest result; every row is a chat.** "New related chat" starts one | Related work stays together, as in Claude Desktop, without rows that aren't chats. |
| D43 | First run | An empty chat | Several "home" chats | **One welcome chat per account: a static message from Bothy with the starters under it** | A first look that explains Bothy, at no cost and without filling the list. |
| D44 | Starters | Guessed on the client | Seven templates | **The server sends up to four rows** for what is set up: two prompts, at most one link to something not set up yet, "What can you do for our team?" | One request, and the copy costs no JavaScript. |
| D45 | Default schedules | Seeded | Seeded | **Examples on the Schedules page, nothing created** until the person saves one; the check-in stays off | Nothing appears in someone's list that they didn't start. |

The second round (2026-10) chose between three directions, "Soft focus", **"Friendly assistant"** and "Crisp pro". The owner picked Friendly; these record what it brought and what was changed on the way in.

| # | Topic | Friendly mockup | Decision | Why |
|---|---|---|---|---|
| D46 | The Friendly look | Oat canvas, white panel, raised selection, bigger titles | **Inset panel** (D3) drawn by a fixed overlay so the document still scrolls; the current nav row is a **raised** white row; page titles 28 px; settings rows in **group cards**; ember (`warning`) for what needs you; moss for the one action and what is on or done | The owner's choice. Each element maps to a token or a recipe, so it is the same on every screen. |
| D47 | Nav icons | Outline at rest, filled when current | **One 18 px outline icon on every nav and settings-nav row**, the current one in `accent-text`; never filled, never on headings, settings rows, menu items or empty states. All icons are one inline SVG sprite in `index.html` (§3.7) | Icons make the destinations scannable. State shown by colour alone keeps one shape per icon; a filled twin would double the set. The sprite costs no JavaScript. |
| D48 | Brand marks | Google "G" and GitHub mark on white buttons | **The official marks on the sign-in buttons and the Account → Sign-in methods rows only**, at 18 and 16 px; Google keeps its four colours, GitHub follows the text colour | The owner asked for them by name; people recognise a provider by its mark before its name. |
| D49 | Bothy's picture | The hound on every Bothy message | **Bothy's picture (the hound unless an admin changed it) at the start of each of Bothy's turns**: 28 px in a 40 px gutter at ≥ 640 px, 20 px above the turn on phones; the logo above the empty-chat title and beside the empty Approvals sentence; nowhere else | The web shows Bothy as the chat apps do. One picture per turn, not per block, keeps the transcript quiet. |
| D50 | Cards and status | Amber border + "Waiting for you" + checks + tinted field box | **At most one status signal per card or row.** In chat it is the waiting line above the card; the card border stays neutral, the card has no status word, fields are a plain key/value grid. Cards get a type icon in a 20 px column | The judge's main concern: four signals at once read as alarm. The type icon says what kind of request it is, which none of the others did. |
| D51 | "Changes saved" | One line in the settings header | **A settings page says "Changes saved" in its header for 2 s after any save**; a row says "Saving…" while it saves and "Saved" only while that header is out of view | One confirmation per page instead of one per row, and one is always in view. |
| D52 | Related-chat breadcrumb | Parent / title in the chat header | **At ≥ 1024 px the chat header leads with the parent chat as a link, then "/"**; phones, and every other origin (schedule, check-in, a deleted chat), keep the line at the top of the transcript | The family is visible where the title is, without a new component; it reads `conv.detail.from`. |

---

## 2. Principles

Every screen review checks against these seven rules.

1. **Content first, one job per screen.** No global top bar and no permanent status chrome. A page leads with its title and its content. A banner appears only when something stops work (§4.9).
2. **Say it like a colleague.** Names, not ids. Money, not tokens. "Bothy wants to run a command", not `shell`. If a sentence needs a config key to make sense, it belongs in Advanced.
3. **Detail on request, never deleted.** Tool names, ids, token counts, rules, raw errors and config keys live one click away behind "Details" or in Settings → Advanced. Anything that leaves Bothy or can't be undone stays on the approval card in full.
4. **One raised level.** Cards, inbox rows, settings groups, option lists, tone cards and the composer sit on the panel with a soft shadow. Never a card inside a card, never a tinted box inside a card (code and diffs excepted). Hairlines only between rows. The logo and Bothy's picture are the only pictures (§3.7); no icon tiles, no subtitle under every heading, no pills.
5. **Colour means something.** Neutral by default. Moss marks the one action and what is on or done. Ember marks what needs you. Red marks failures and the destructive click. Hover and selection are warm grey; the current nav row is a raised white row.
6. **Honest record.** Tense matches reality: "Waiting for approval to delete…", "Deleting…", "Deleted", "Didn't delete". Decisions stay in the transcript and in Activity. Cost is shown before an expensive action, in dollars.
7. **The same everywhere.** One column, one left edge, one control height, one button system. Keyboard, touch, 360 px, light and dark are designed states. Shortcut hints never show on touch devices. Nothing relies on colour alone. At most one status signal per card or row.

### 2.1 The "AI-generated" tells and what replaces them

| Tell (v0.1) | Where (audit screen) | Replacement |
|---|---|---|
| Icon in a tinted rounded square | Logo, Done, every empty state, chat-app rows, sandbox card (`11`, `12`, `16`, `21`–`23`, `74`, `75`) | Wordmark "Bothy" in text beside the mascot (§3.7), the one picture in the chrome. Empty states are text plus one button (Approvals → Waiting adds the logo beside it). Icons only where §3.7 places them: controls, states next to words, nav rows and approval types. |
| Pills and chips (105 `chip` usages) | Tool rows, approval cards, audit "ok", `default` under 50 fields, provenance chips, "disabled" said four ways (`24`, `29`, `47`, `60`–`65`, `71`) | `.chip` deleted, no badge component. A status is a word, coloured only when abnormal. Provenance only in Advanced, only for changed values. |
| Cards inside cards | Wizard, settings sections, approvals focus ring, Usage, Memory, schedule edit, run trace (`10`, `29`, `40`, `43`, `46`, `51`) | Approval cards, option lists and overlays are the only bordered containers. Code blocks and diffs use a tint, not a border. |
| Sparkle for "model", decorative icons | Settings nav, run trace (`51`, `61`) | Nav rows carry one 18 px outline icon, the current one in moss; no sparkles, no tiles, no icons on headings, settings rows, menu items or empty states. Models are called by name. |
| A reassurance sentence under every heading | Most settings pages | Headings stand alone. Help text only when it changes what the person does next. |
| Four KPI cards above a one-bar chart | Usage, run trace (`46`, `51`) | One sentence with the total, a neutral bar chart, plain lists. |
| 11-colour legend, 17 hues | Context drawer (`34`), `app.css` | Neutrals plus green, red and amber. Breakdowns are tables behind Details. |
| Backdrop blur | Top bar, every dialog (`34`–`39`) | No blur, no translucency. Solid scrim behind dialogs. |
| Generic starter prompts | "Help me plan my week" (`23`) | Three company-work suggestions based on what is connected. |
| Monospace for prose | Memory, persona, durations, ids (`40`, `68`) | Sans for everything people write and read. Mono only for code, diffs, commands, paths, hosts and branches. |
| Keyboard chips on buttons, also on touch | Approvals (`29`, `28` at 360) | Shortcuts in tooltips on fine pointers and in one "Keyboard shortcuts" dialog. |
| Two filled "New chat" buttons, a heavy green app | Chat home (`20`, `23`) | One "New chat" sidebar item. At most one filled button per view. |
| Personal-assistant voice | Setup, persona ("Household", "your own assistant") | Company voice (§14). |
| Filled and outline icon pairs | Friendly mockup's current nav item, Refine, checks | Colour only: `text-accent-text` on the outline icon. Lint bans `fill-*` and inline `<svg>` outside `Icon.svelte`. |
| Stacked status signals (coloured border + status word + glyphs + tint) | Friendly mockup's approval card | One signal per card or row (D50). |
| A streaming caret or dot | Friendly mockup's reply | Nothing (D35). Lint bans `animate-pulse`, `-ping` and `-bounce`. |
| Web fonts | All three mockups (Inter) | The system stack (§3.2); `font-src 'self'` would block them anyway. Lint bans `@font-face`. |
| Links to flows that don't exist ("Forgot it?") | Friendly mockup's sign-in | Only real paths: "Forgot your password? Ask an admin." |
| Mock states the product can't reach (Bothy replying while its card waits) | Friendly mockup's chat | Shots come from the fixture gateway only; a waiting card ends its turn. |

---

## 3. Design tokens

All tokens live in `web/src/app.css` inside `@theme`. In the final state (chunk C10) Tailwind's default colours, font sizes, radii and shadows are reset with `initial`, so only these tokens compile (`bg-green-500`, `text-3xl` and `rounded-2xl` stop working). During migration the resets are off and legacy aliases keep v0.1 screens rendering (§18, chunk F1).

### 3.1 Colour

Three hues: moss (accent), ember (warning, "needs you") and red (danger). Everything else is warm neutral. Ratios are WCAG 2.x, computed from the hex values; `web/src/tokens.test.ts` checks every pair below in both themes.

**Light**

| Token | Hex | Used for | Contrast |
|---|---|---|---|
| `bg` | `#FFFDFA` | The page panel | text 16.6 |
| `sidebar` | `#F4F0E8` | The oat canvas: sidebar, the frame around the panel, the sign-in page | text 14.8, muted 5.4 |
| `surface` | `#FFFFFF` | Cards, groups, dialogs, menus, inputs, composer | text 16.8, muted 6.1 |
| `raised` | `#FFFFFF` | Current nav row, New chat, the open chat row, tab and segment thumbs | text 16.8, accent-text 6.4, warning-text 5.9 |
| `subtle` | `#F4EEE4` | User message, inline code, code blocks, notices, hover on cards and rows, the tab track | text 14.6, muted 5.3, faint 4.6 |
| `hover` | `#ECE6DB` | Hover on sidebar rows | muted 4.9 |
| `selected` | `#E9E2D5` | Selected list row | text 13.1, muted 4.8 |
| `border` | `#ECE5D9` | Hairlines, card and group edges | decorative |
| `border-strong` | `#DDD4C5` | Composer, secondary button, tone cards, the related-chats guide line | decorative |
| `control` | `#958E82` | Input, checkbox and radio borders, switch track off | 3.2 on bg and surface |
| `text` | `#1F1D1A` | Body text, headings, nav labels | |
| `muted` | `#67615A` | Secondary text, help, meta, nav icons | 6.0 on bg |
| `faint` | `#716B61` | Placeholders and disabled text only, never information | 5.2 on bg |
| `accent` | `#2A6A54` | Primary button fill, switch on, send, meter fill | white on it 6.4 |
| `accent-hover` | `#225A47` | Primary hover | white 8.0 |
| `accent-active` | `#1C4B3B` | Primary pressed | white 9.9 |
| `accent-text` | `#2A6A54` | Links, the current nav icon, New chat's icon, Refine on, success check, `+` in diffs | 6.3 on bg, 5.6 on sidebar |
| `accent-subtle` | `#E6F0EA` | Refine on, the chosen tone card | accent-text 5.5, muted 5.3 |
| `on-accent` | `#FFFFFF` | Text and icons on `accent` and `danger`, switch thumb | |
| `focus` | `#2A6A54` | Focus ring and the field focus border | 6.3 on bg |
| `danger` | `#B42318` | Destructive button fill | white 6.6 |
| `danger-hover` | `#9A1D14` | Destructive hover | white 8.2 |
| `danger-text` | `#B42318` | Error text and glyphs, destructive menu items, `−` in diffs | 6.5 on bg |
| `danger-subtle` | `#FCEDEB` | Danger notice and banner, removed diff lines | danger-text 5.8, text 14.8 |
| `warning` | `#C2660F` | Ember: the attention dot and the Approvals count dot. Shapes only, never text | 3.6 on sidebar, 3.1 on selected |
| `warning-text` | `#97530F` | The Approvals count, "{n} waiting", "Expires in 1 min", the waiting line | 5.8 on bg, 5.2 on sidebar |
| `warning-subtle` | `#FCEFDF` | Warning notice and banner, search `<mark>` | warning-text 5.2, text 14.9 |
| `diff-add` | `#EAF4EC` | Added diff lines, inserted prose | text 14.9 |
| `chart` | `#958E82` | Chart bars | 3.2 on bg |
| `scrim` | `rgb(31 24 14 / 0.40)` | Dialog and drawer backdrop | |

**Dark**

| Token | Hex | Contrast |
|---|---|---|
| `bg` | `#1A1917` | text 14.8 |
| `sidebar` | `#121110` | text 15.9, muted 7.0 |
| `surface` | `#211F1D` | text 13.8, muted 6.1 |
| `raised` | `#2A2826` | text 12.3, accent-text 7.9, warning-text 8.1 |
| `subtle` | `#272522` | muted 5.7, faint 4.8 |
| `hover` | `#2F2D2A` | muted 5.1 |
| `selected` | `#35322E` | text 10.7, muted 4.7 |
| `border` | `#2E2B28` | |
| `border-strong` | `#3D3A36` | |
| `control` | `#726D65` | 3.4 on bg, 3.2 on surface |
| `text` | `#EEEBE5` | |
| `muted` | `#A39D93` | 6.5 on bg |
| `faint` | `#949087` | 5.5 on bg, 5.2 on surface |
| `accent` | `#2F7D61` | white on it 5.0; fill vs bg 3.5 |
| `accent-hover` | `#296F56` | white 6.0 (hover darkens, so the button never glows) |
| `accent-active` | `#235F4A` | white 7.5 |
| `accent-text` | `#86CDAE` | 9.5 on bg |
| `accent-subtle` | `#1B2D25` | accent-text 7.8 |
| `on-accent` | `#FFFFFF` | |
| `focus` | `#86CDAE` | 9.5 on bg |
| `danger` | `#B4372D` | white 6.0 (deep red, not salmon) |
| `danger-hover` | `#A3332A` | white 6.9 |
| `danger-text` | `#F0938A` | 7.7 on bg |
| `danger-subtle` | `#3A221F` | danger-text 6.5, text 12.4 |
| `warning` | `#E89A4C` | 8.2 on sidebar, 5.6 on selected |
| `warning-text` | `#F0B673` | 9.7 on bg |
| `warning-subtle` | `#30251A` | warning-text 8.3 |
| `diff-add` | `#1E2E22` | text 12.0 |
| `chart` | `#726D65` | 3.4 on bg |
| `scrim` | `rgb(0 0 0 / 0.60)` | |

Rules:
- At most one filled button per view (a dialog is its own view; each approval card may have its own).
- `faint` never carries information. Timestamps, help, meta and the ⌘K hint use `muted`.
- `warning` is for shapes only (dots); text in ember is `warning-text`. Lint bans `text-warning` without a suffix.
- Red fill only on the button that performs a destructive action. Deny and Cancel are never red.
- A status word is `muted` when the state is normal ("Connected", "Up to date"), `warning-text` when it needs attention soon, `danger-text` when broken. A success line is a check in `accent-text` plus ink text, shown only at the moment something succeeded.
- Links are `accent-text` with a permanent underline at 40 % opacity (100 % on hover), so they never rely on colour.
- Charts use `chart` for bars, `text` for the hovered or focused bar, `border` for gridlines.
- Removed from v0.1: `info`, `info-soft`, `ok`, `ok-soft`, the six `risk-*` colours, `r1`–`r6`, `sunken`, `line`, `line-strong`, `fg`.

### 3.2 Type

System fonts, no web fonts (`font-src 'self'`; lint bans `@font-face`). Sans: `ui-sans-serif, system-ui, -apple-system, 'Segoe UI', Roboto, 'Helvetica Neue', Arial, sans-serif`. Mono: `ui-monospace, SFMono-Regular, 'SF Mono', Menlo, Consolas, 'Liberation Mono', monospace`.

| Utility | Size / line height | Weight | Use |
|---|---|---|---|
| `text-caption` | 12 / 16 | 500 | Sidebar group labels, chart axis labels, day separators, keys in the shortcuts dialog. Never sentences. |
| `text-small` | 13 / 18 | 400 | Help, meta lines, status words, card field labels, toasts on phones |
| `text-body` | 14 / 20 | 400, 500 | UI default: rows, buttons (500), inputs, labels (500), menus, settings |
| `text-reading` | 15 / 24 | 400 | Chat messages, project notes, memory, setup body copy, empty states |
| `text-heading` | 15 / 22 | 600 | Section headings, approval questions, dialog titles, chat title |
| `text-figure` | 22 / 28 | 600, -0.01em | Money totals: the Spending total, today's spending on General |
| `text-title` | 28 / 34 | 600, -0.022em | Page titles, sign-in and setup titles, the empty-chat heading |
| `text-code` | 13 / 20 mono | 400 | Code, diffs, commands, paths, hosts, branches |

`text-display` is gone; lint bans it.

Rules:
- Weights 400, 500 and 600 only. No 700, no uppercase, no letter-spaced labels, no italics in UI chrome.
- At most two sizes within any 40 px vertical band.
- Money, counts and times in lists use `tabular-nums`.
- Inputs, textareas and the composer use 16 px under `(pointer: coarse), (max-width: 639px)` so iOS Safari does not zoom.
- Markdown headings in chat and notes map down: `#` and `##` → `text-heading` with 20 px above, `###` and below → `text-body` 600 with 16 px above. A reply never produces a heading larger than 15 px.
- Mono is not allowed for durations, money, ids shown to people, model names or anything a person wrote.

### 3.3 Spacing and layout

Base unit 4 px (Tailwind's default `--spacing: 0.25rem`). Allowed steps: 1, 2, 3, 4, 5, 6, 8, 10, 12, 16 (4 to 64 px). Half steps (`0.5`, `1.5`, `2.5`) only for optical icon alignment.

| Constant | Value | Tailwind |
|---|---|---|
| Sidebar (≥ 1024 px) | 256 px | `w-64` |
| Content column (every page and the chat) | 768 px max including 24 px side padding, so 720 px of content; 16 px padding under 640 px | `mx-auto w-full max-w-3xl px-6 max-sm:px-4` |
| Setup column | 480 px max, centred | `max-w-[30rem]` |
| Page top padding | 40 px; 24 px under 640 px | `pt-10 max-sm:pt-6` |
| Page title → content (or → tabs) | 24 px (16 px to tabs) | `mt-6` / `mt-4` |
| Section → section | 40 px | `mt-10` |
| Section heading → first row | 8 px | `mt-2` |
| Control height | 36 px; small 32 px; 44 px on `pointer: coarse` | `h-9`, `h-8`, `pointer-coarse:h-11` |
| List row | 40 px one line, 56 px two lines; 48 px minimum on coarse pointers | `min-h-10`, `min-h-14` |
| Settings row | 56 px minimum | `min-h-14 py-3` |
| Sidebar nav row | 36 px; 44 px on coarse pointers | `h-9` |
| Sidebar chat row | 32 px; 44 px on coarse pointers | `h-8` |
| Inset (the panel's margin from the window, ≥ 1024 px) | 8 px; 0 below | `--inset` |
| Sign-in card | 424 px wide, 36 px padding; no card under 640 px | `max-w-[26.5rem] sm:p-9` |
| Chat header, phone top bar | 52 px | `h-13` |
| Dialog widths | 440 (confirm), 560 (form), 720 (review: diffs, notes) | `max-w-[27.5rem]`, `max-w-[35rem]`, `max-w-[45rem]` |
| Drawer | 400 px; full screen under 640 px | `w-100` |

**One left edge.** At 1280 px the main area is 1024 px wide, the column is centred with 128 px either side, and content starts at **x = 408** on every route, chat included. At 1024 px it starts at x = 280. Below 1024 px the sidebar becomes a drawer and content starts at 24 px (16 px under 640 px). Wide data never widens the column; rows fold meta into a second line (§13.3).

**Breakpoints:** phone < 640 px, tablet 640–1023 px (sidebar is a drawer, desktop layouts otherwise), desktop ≥ 1024 px (sidebar pinned). Input mode is separate from width: `pointer-coarse:` grows targets to 44 px and makes Enter insert a newline in the composer; hover-only affordances and shortcut hints exist only with `(hover: hover) and (pointer: fine)` (Tailwind's `hover:` already applies only there; use `pointer-fine:` for hints).

### 3.4 Radii, borders, elevation

| Utility | Value | Use |
|---|---|---|
| `rounded-sm` | 6 px | Inline code, `<mark>`, tooltips, checkboxes |
| `rounded-md` | 10 px | Buttons, inputs, selects, nav and chat rows, menu items, code blocks, notices, tab and segment thumbs |
| `rounded-lg` | 14 px | The page panel, cards, groups, inbox rows, option lists, dialogs, menus, composer, user messages, tone cards, the tab track |
| `rounded-xl` | 20 px | The sign-in card only (lint allows it in `setup/Frame.svelte` and nowhere else) |
| `rounded-full` | — | Avatars, Bothy's picture in chat, switch track and thumb, spinner, the dots, the meter. Nothing else. |

Always use a named radius; bare `rounded` is not allowed.

- Borders are 1 px. `border` for hairlines and the edges of cards and groups, `border-strong` for the composer, secondary buttons and tone cards, `control` for form controls. The only 2 px lines are the focus outline.
- Three elevation levels, all from named shadows (lint bans literal `shadow-[…]`; the G4 probe accepts only these values):
  - **Flat:** page content that is not a card: headings, text, rows inside a group.
  - **Raised:** `shadow-raised` on the current nav row, New chat, the open chat row and tab and segment thumbs; `shadow-card` on cards, groups, inbox rows and the composer; `shadow-primary` on the primary button.
  - **Floating:** `shadow-overlay` on menus, popovers, tooltips, toasts, dialogs and drawers (with a `border`); `shadow-pop` on the sign-in card.
  - `shadow-frame` draws the inset panel's edge and the canvas around it (§6.1), on that overlay only. `shadow-focus` is the field focus halo (§3.5).
  - Light uses warm brown shadows (`rgb(74 58 33 / …)`); dark uses black ones with a 1 px ring (`#262422`–`#34312D`) and a faint top highlight, so raised things read without glowing. The values are in §3.8.
  - In `@theme` each `--shadow-*` is `var(--elevation-*)`, and the light and dark values are `--elevation-*` in the base layer: Tailwind copies a literal theme shadow into its utility, so a dark override of `--shadow-*` would never reach `shadow-card`. `tokens.test.ts` checks the indirection.
- No `backdrop-filter`, no translucent surfaces, no gradients (the composer sits on a solid `bg` strip, not a fade).

### 3.5 Focus

- Every focusable element: `:focus-visible { outline: 2px solid var(--color-focus); outline-offset: 2px }`.
- Text inputs, selects and textareas show focus on mouse focus too (`:focus`), because a field must show where typing goes.
- Rows in lists (settings rows, list rows, sidebar rows) use `outline-offset: -2px`, so focus draws inside the row and never looks like a second card. Inbox rows are cards themselves, so their focus draws outside them (`outline-offset: 2px`). A row opened by a link stretched over it (`.row-stretch`) draws that ring around the whole row only while that link has focus; a control of its own on the row (a switch, Review, a GitHub link) shows only its own ring.
- Inputs and the composer show focus as a `focus` border plus a 4 px halo (`shadow-focus`, accent at 14 % in light, 18 % in dark). A transparent 2 px outline stays underneath, so forced-colours mode, which drops shadows, still draws a ring.
- Focus moves on purpose: a dialog focuses its first field, or Cancel in a destructive dialog; closing returns focus to the trigger; after an approval decision focus goes to the next waiting item or to the decided line, and a polite live region announces the result.

### 3.6 Motion

| Change | Duration | Easing | What moves |
|---|---|---|---|
| Hover, pressed, colour | 120 ms | linear | background, colour, border colour |
| Disclosure open and close | 160 ms | `--ease-out` `cubic-bezier(0.2, 0, 0, 1)` | `grid-template-rows: 0fr → 1fr`, opacity |
| Menu, popover, tooltip in | 120 ms | `--ease-out` | opacity, translateY 4 px → 0 |
| Dialog in / out | 180 / 120 ms | `--ease-out` / `--ease-in` `cubic-bezier(0.4, 0, 1, 1)` | opacity, scale 0.98 → 1 |
| Drawer | 200 ms | `--ease-out` | translateX |
| Toast in | 160 ms | `--ease-out` | opacity, translateY 8 px → 0 |
| Decided inbox row | 160 ms | `--ease-out` | row collapses to 0 height |

- The only looping animation is the 12 or 16 px spinner, one turn per 800 ms, linear.
- No motion on streaming text, new messages or list updates: no caret, no dot after the text (D35). No page transitions, skeleton shimmer, pulsing, bouncing or count-ups; lint bans `animate-pulse`, `animate-ping` and `animate-bounce`.
- "Loading…" appears only after 400 ms.
- `prefers-reduced-motion: reduce` sets every duration to 0 and replaces the spinner with a static "…".

### 3.7 Icons

43 icons in `lib/icons.ts`: 41 outline icons drawn on a 24 grid with a 1.5 px round stroke in `currentColor`, and two brand marks.

- **Kept from v0.2 (18):** `plus`, `search`, `chevron-down`, `chevron-right`, `more`, `close`, `check`, `alert`, `clock`, `arrow-up` (send), `stop`, `paperclip`, `copy`, `external`, `menu`, `back`, `refresh`, `spinner`.
- **Added for the Friendly look (23):** `compose`, `approvals`, `knowledge`, `activity`, `settings`, `user`, `sliders`, `models`, `plug`, `users`, `shield`, `code`, `target`, `pull-request`, `mail`, `deploy`, `terminal`, `file`, `globe`, `calendar`, `upload`, `eye`, `eye-off`.
- **Brand marks (2):** `google` (the official four-colour "G", never recoloured) and `github` (the Invertocat in `currentColor`: ink in light, near-white in dark). They follow the providers' sign-in guidelines.

**Mechanism.** `vite.config.ts` writes every icon into `index.html` as one `<svg id="icons" aria-hidden="true">` of `<symbol id="i-{name}">` at build and dev time; `Icon.svelte` draws `<svg><use href="#i-{name}"></svg>` with the stroke settings on the outer `<svg>`, so they inherit through `<use>`. Outline paths keep `vector-effect="non-scaling-stroke"` (1.5 CSS px at any size); brand symbols carry `stroke="none"` so the outer stroke never outlines them. The shapes cost no JavaScript (components import only the `IconName` type) and about 2.5 KB of gzip in `index.html`, which is served `no-store` with no extra request. The sprite has no `style` or script, so the CSP allows it. Under jsdom `<use>` draws nothing; tests read the `href`.

**Sizes.** 16 px inline and in small buttons; 18 px on nav rows, approval types, the brand buttons and the eye toggle's neighbours; 20 px inside 36 px icon buttons; 12 px for disclosure chevrons.

**State is colour.** An icon is never filled to show state. The current nav row's icon and New chat's icon are `text-accent-text`; every other nav icon is `text-muted`. Lint bans `fill-*` utilities and any `<svg>` in a component other than `Icon.svelte`.

**Placements** (the only places icons appear; a new icon needs a row here):

| Where | Icons | Size, colour |
|---|---|---|
| Sidebar | New chat `compose`, Search `search`, Approvals `approvals`, Knowledge `knowledge`, Activity `activity`, Settings `settings` | 18; `text-muted`, current row and New chat `text-accent-text` |
| Chat rows | family toggle `chevron-right`, row ⋯ `more`, running `spinner` (12) | as before |
| Settings navigation and the phone list of groups | ← Back to Bothy `back`; Account `user`, General `sliders`, Models `models`, Connections `plug`, People `users`, Security `shield`, Advanced `code` | 18, same rule as the sidebar |
| Phone top bar and drawer | `menu`, `close` | 20 |
| Chat header | `more` (Chat options) | 20 |
| Reply actions | `copy`, `refresh`, `more` | 16 |
| Composer | Attach `paperclip`, Refine `target` (moss when on), model `chevron-down`, Stop `stop`, Send `arrow-up` | 16 |
| Activity and decided lines | `spinner`, `clock` (`warning-text`), `alert` (`danger-text`), `close` (muted), `check` (accent) on decided lines only; done steps have none (D11) | 16 |
| Approval card, review card and inbox row type | `pull-request` (repo_open_pr, github_comment), `deploy` (release_deploy), `terminal` (shell, python), `file` (fs_*, notes, memory, skill_*; skill reviews), `mail` (message), `globe` (web_fetch, net_allow), `calendar` (schedule_*), `plug` (MCP actions and reviews), `users` (someone new, a new group chat), else `approvals` | 18, `text-muted`; hidden on cards under 640 px |
| "Waiting for others" row status | `clock` | 16, muted |
| A row that opens a page or expands in place, with no control of its own at its end (inbox rows, Examples, Tasks, Projects, a member's Skills, Spending and release-history rows that link, the audit log) | `chevron-right` at the end; turned down while an audit row is open | 16, muted |
| Sign-in | provider buttons `google` / `github`; password field `eye` / `eye-off` | 18 / 16 |
| Account → Sign-in methods rows | `google` / `github` | 16 |
| General → Picture "Change…" | `upload` | 16 |
| Settings header "Changes saved" | `check` | 16, `accent-text` |
| The chosen tone card | `check` | 16, `accent-text` |
| Disclosures, selects, menus | `chevron-right` (12), `chevron-down`, `check` in menu gutters | 12 / 16 |

**Never:** on headings, settings rows, menu items, empty states, tiles, approval buttons, banners or toasts. No filled variants and no icon-only status. Every icon-only button has an `aria-label` and a tooltip with the same text.

**Pictures.** The logo, the mascot (a white robot hound with a dark visor and warm eyes on a near-black tile, `public/icon.svg`), sits at 28 px beside the wordmark "Bothy" (`text-heading`) in the sidebar, 56 px on the sign-in card, 28 px on the setup frame, 40 px above the empty-chat title and 32 px beside the empty Approvals sentence; in dark mode it gets a 1 px `border-strong` ring where it sits on a card. It is also the favicon and the manifest's icons; `icon-maskable.svg` is the same dog full bleed for the maskable and Apple touch icons and Bothy's default chat-app picture. `make icons` renders the PNGs. **Bothy's picture** (`/api/agent/picture`, the default unless an admin changed it) is round, 28 px in chat (D49) and 48 px in Settings → General. Neither is an icon; nothing else sits on a tile.

### 3.8 `app.css` (final state)

The `@custom-variant`, `@theme` and `@layer base` blocks of `web/src/app.css` as they ship. Component recipes are in §15.2.

```css
@import 'tailwindcss' source('./');

/* theme.js resolves "system" into data-theme before first paint, so no media-query branch is needed. */
@custom-variant dark (&:where([data-theme='dark'], [data-theme='dark'] *));

@theme {
  --color-*: initial;
  --text-*: initial;
  --radius-*: initial;
  --shadow-*: initial;

  --font-sans: ui-sans-serif, system-ui, -apple-system, 'Segoe UI', Roboto, 'Helvetica Neue', Arial, sans-serif;
  --font-mono: ui-monospace, SFMono-Regular, 'SF Mono', Menlo, Consolas, 'Liberation Mono', monospace;

  --color-bg: #fffdfa;
  --color-sidebar: #f4f0e8;
  --color-surface: #ffffff;
  --color-raised: #ffffff;
  --color-subtle: #f4eee4;
  --color-hover: #ece6db;
  --color-selected: #e9e2d5;
  --color-border: #ece5d9;
  --color-border-strong: #ddd4c5;
  --color-control: #958e82;
  --color-text: #1f1d1a;
  --color-muted: #67615a;
  --color-faint: #716b61;
  --color-accent: #2a6a54;
  --color-accent-hover: #225a47;
  --color-accent-active: #1c4b3b;
  --color-accent-text: #2a6a54;
  --color-accent-subtle: #e6f0ea;
  --color-on-accent: #ffffff;
  --color-focus: #2a6a54;
  --color-danger: #b42318;
  --color-danger-hover: #9a1d14;
  --color-danger-text: #b42318;
  --color-danger-subtle: #fcedeb;
  --color-warning: #c2660f;
  --color-warning-text: #97530f;
  --color-warning-subtle: #fcefdf;
  --color-diff-add: #eaf4ec;
  --color-chart: #958e82;
  --color-scrim: rgb(31 24 14 / 0.4);

  --text-caption: 0.75rem;
  --text-caption--line-height: 1rem;
  --text-caption--font-weight: 500;
  --text-small: 0.8125rem;
  --text-small--line-height: 1.125rem;
  --text-body: 0.875rem;
  --text-body--line-height: 1.25rem;
  --text-reading: 0.9375rem;
  --text-reading--line-height: 1.5rem;
  --text-heading: 0.9375rem;
  --text-heading--line-height: 1.375rem;
  --text-heading--font-weight: 600;
  --text-figure: 1.375rem;
  --text-figure--line-height: 1.75rem;
  --text-figure--font-weight: 600;
  --text-figure--letter-spacing: -0.01em;
  --text-title: 1.75rem;
  --text-title--line-height: 2.125rem;
  --text-title--font-weight: 600;
  --text-title--letter-spacing: -0.022em;
  --text-code: 0.8125rem;
  --text-code--line-height: 1.25rem;

  --radius-sm: 6px;
  --radius-md: 10px;
  --radius-lg: 14px;
  --radius-xl: 20px;

  --shadow-raised: var(--elevation-raised);
  --shadow-card: var(--elevation-card);
  --shadow-primary: var(--elevation-primary);
  --shadow-focus: var(--elevation-focus);
  --shadow-frame: var(--elevation-frame);
  --shadow-pop: var(--elevation-pop);
  --shadow-overlay: var(--elevation-overlay);

  --ease-out: cubic-bezier(0.2, 0, 0, 1);
  --ease-in: cubic-bezier(0.4, 0, 1, 1);

  --animate-spin: spin 800ms linear infinite;
}

@layer base {
  /* Tailwind copies a theme value into its shadow-* utility, so the themed values live here and @theme points at them. */
  :root {
    color-scheme: light;
    --elevation-raised: 0 0 0 1px rgb(74 58 33 / 0.06), 0 1px 2px rgb(74 58 33 / 0.08);
    --elevation-card: 0 1px 2px rgb(74 58 33 / 0.05), 0 6px 16px -8px rgb(74 58 33 / 0.12);
    --elevation-primary: inset 0 1px 0 rgb(255 255 255 / 0.12), 0 1px 2px rgb(20 60 45 / 0.28);
    --elevation-focus: 0 0 0 1px var(--color-focus), 0 0 0 4px rgb(42 106 84 / 0.14);
    --elevation-frame: 0 0 0 1px rgb(74 58 33 / 0.07), 0 12px 32px -16px rgb(74 58 33 / 0.14), 0 0 0 100vmax var(--color-sidebar);
    --elevation-pop: 0 0 0 1px rgb(74 58 33 / 0.07), 0 24px 48px -20px rgb(60 44 20 / 0.28);
    --elevation-overlay: 0 1px 2px rgb(74 58 33 / 0.06), 0 8px 24px rgb(74 58 33 / 0.14);
  }
  :root[data-theme='dark'] {
    color-scheme: dark;
    --color-bg: #1a1917;
    --color-sidebar: #121110;
    --color-surface: #211f1d;
    --color-raised: #2a2826;
    --color-subtle: #272522;
    --color-hover: #2f2d2a;
    --color-selected: #35322e;
    --color-border: #2e2b28;
    --color-border-strong: #3d3a36;
    --color-control: #726d65;
    --color-text: #eeebe5;
    --color-muted: #a39d93;
    --color-faint: #949087;
    --color-accent: #2f7d61;
    --color-accent-hover: #296f56;
    --color-accent-active: #235f4a;
    --color-accent-text: #86cdae;
    --color-accent-subtle: #1b2d25;
    --color-focus: #86cdae;
    --color-danger: #b4372d;
    --color-danger-hover: #a3332a;
    --color-danger-text: #f0938a;
    --color-danger-subtle: #3a221f;
    --color-warning: #e89a4c;
    --color-warning-text: #f0b673;
    --color-warning-subtle: #30251a;
    --color-diff-add: #1e2e22;
    --color-chart: #726d65;
    --color-scrim: rgb(0 0 0 / 0.6);
    --elevation-raised: 0 0 0 1px #34312d, inset 0 1px 0 rgb(255 255 255 / 0.03);
    --elevation-card: inset 0 1px 0 rgb(255 255 255 / 0.03), 0 1px 2px rgb(0 0 0 / 0.3);
    --elevation-primary: inset 0 1px 0 rgb(255 255 255 / 0.1), 0 1px 2px rgb(0 0 0 / 0.4);
    --elevation-focus: 0 0 0 1px var(--color-focus), 0 0 0 4px rgb(134 205 174 / 0.18);
    --elevation-frame: 0 0 0 1px #262422, 0 12px 32px -16px rgb(0 0 0 / 0.6), 0 0 0 100vmax var(--color-sidebar);
    --elevation-pop: 0 0 0 1px #34312d, 0 24px 48px -20px rgb(0 0 0 / 0.7);
    --elevation-overlay: 0 0 0 1px #34312d, 0 8px 24px rgb(0 0 0 / 0.5);
  }
  /* The sticky banner (--bar, set by the shell) and the phone top bar cover the page's top edge,
     so what is scrolled or focused into view lands below them. */
  :root {
    --inset: 0px;
  }
  html {
    -webkit-text-size-adjust: 100%;
    scroll-padding-top: calc(var(--bar, 0px) + var(--inset));
  }
  @media (width >= 64rem) {
    :root {
      --inset: 0.5rem;
    }
  }
  @media (width < 64rem) {
    html {
      scroll-padding-top: calc(3.25rem + var(--bar, 0px));
    }
  }
  #icons {
    position: absolute;
    width: 0;
    height: 0;
    overflow: hidden;
  }
  body {
    background: var(--color-bg);
    color: var(--color-text);
    font-family: var(--font-sans);
    font-size: var(--text-body);
    line-height: var(--text-body--line-height);
    -webkit-font-smoothing: antialiased;
  }
  :focus-visible {
    outline: 2px solid var(--color-focus);
    outline-offset: 2px;
  }
  /* Route changes move focus to the page heading for screen readers (§16); it is not a control, so it draws no ring. */
  :is(h1, h2)[tabindex='-1']:focus-visible {
    outline: none;
  }
  ::selection {
    background: var(--color-accent-subtle);
  }
  [hidden] {
    display: none !important;
  }
  dialog::backdrop {
    background: var(--color-scrim);
  }
  @media (pointer: coarse), (max-width: 639px) {
    input,
    textarea,
    select {
      font-size: 16px;
    }
  }
  @media (prefers-reduced-motion: reduce) {
    *,
    *::before,
    *::after {
      animation: none !important;
      transition-duration: 0.01ms !important;
    }
  }
}
```

Notes for the implementer:
- With `--color-*: initial`, `white` and `black` disappear on purpose; use `on-accent`, `bg` or `text`.
- The text colour utility is `text-text`. Do not alias it.
- `index.html`'s `theme-color` metas are `#FFFDFA` (light) and `#1A1917` (dark), and the manifest's colours `#FFFDFA`: the colour of the page on a phone, where there is no inset.

---

## 4. Information architecture and navigation

### 4.1 The map

```
Sidebar
├── New chat                      /chat
├── Search                        dialog (⌘K / Ctrl K)
├── Approvals            2        /approvals, /approvals/:id
├── Knowledge                     /knowledge                   Projects (default tab)
│                                 /knowledge/memory            Memory
│                                 /knowledge/skills            Skills
├── Activity                      /activity                    Tasks (default tab)
│                                 /activity/schedules          Schedules (incl. Check-in)
│                                 /activity/releases           Releases (once an environment exists)
│                                 /activity/spending           Spending
│                                 /activity/audit              Audit log (admins)
├── Today / Previous 7 days / Older                  /chat/:id
└── Settings (admins)  •          /settings/general …          General, Models, Connections, People, Security, Advanced
Account menu (footer)             Your account (/settings/account), Theme, Keyboard shortcuts, Pause Bothy… (admins), Sign out
Outside the app                   /setup, /login
```

Four destinations (Chat, Approvals, Knowledge, Activity) plus Settings, where v0.1 had 7 rail items, a second chat column and 13 settings pages.

### 4.2 Sidebar (≥ 1024 px)

```
┌──────────────────────────┐
│ ▣ Bothy                  │  48 px row, 28 px mascot and wordmark text-heading, links to /chat
│ ✎ New chat               │  always a raised white row, compose icon in moss (not a filled button)
│ ⌕ Search            ⌘K   │  hint text-caption muted, fine pointer only
│ ☑ Approvals         • 2  │  6 px ember dot + count in warning-text; hidden at 0
│ ▯ Knowledge              │
│ ↺ Activity               │  outline icons 18 px, muted; the current row raised, its icon moss
│                          │
│ Today                    │
│ Fix login redirect     ◌ │  12 px spinner while Bothy is working there
│ v2.3 launch checklist  • │  8 px warning dot: waiting for approval
│ Q3 pricing notes     ›   │  a family's head: the chat others continued from
│ PR summary · 19 Oct  ⌄   │  a schedule's newest result, open
│  │ 2 h ago               │  its earlier results, by date, on a guide line
│  │ 12 Oct                │
│  │ Chase the reviews     │  a chat continued from one of them, by title
│ Previous 7 days          │
│ Onboarding checklist     │
│ Show more                │  after 30 rows, text-muted
│ ──────────────────────── │  border-t border-border, pinned to the bottom
│ ⚙ Settings             • │  admins only; attention dot (§4.10)
│ (MC) Maya Chen         ⋯ │  avatar 24, name text-body 500, ⋯ opens the account menu
└──────────────────────────┘
```

- Container: 256 px, full height, `sticky top-0 z-40 h-dvh`, `bg-sidebar` (the canvas), `px-2`, own scroll, no edge line: the canvas meets the page panel's frame (§6.1). The phone drawer (§4.4) has the same rows; there titles get about 200 px, with the full title in the tooltip and the chat's header.
- Nav rows (New chat, Search, Approvals, Knowledge, Activity, Settings): `h-9 rounded-md px-2.5 gap-2.5 text-body font-medium text-text` (44 px on coarse pointers), each led by its 18 px outline icon (§3.7) in `text-muted`. Hover `bg-hover`. The current row is `bg-raised shadow-raised` with `aria-current="page"` and its icon in `text-accent-text`. New chat is always raised, with its `compose` icon in `text-accent-text`: starting a chat is the sidebar's main action. Labels stay ink; only the icon changes colour (D47).
- The Approvals count: a 6 px ember dot (`Dot size={6}`) and the number in `text-small font-medium text-warning-text tabular-nums`, then visually hidden " waiting"; nothing at 0 (D23).
- Group labels: `text-caption text-muted`, 16 px above, 4 px below.
- Chat rows: `h-8 rounded-md px-2.5`, one line, truncate with a `title` tooltip. The open chat (and the head of a closed family holding it) is `bg-raised shadow-raised`; hover `bg-hover`. Running: spinner at the right. Waiting for approval: 8 px ember dot with visually hidden "Waiting for approval". On hover (always on coarse pointers) a `⋯` replaces the trailing slot with Rename and Delete chat….
- No origin icons. A chat that came from a chat app says so in its header (§7.1), not in the list.
- No group chats. Chats in chat apps belong to the people and channels there (SPEC §5.2), not to anyone's account, so they are in nobody's list, admins' included.
- Chats are stored only once a message is sent. Until the generated title arrives the server titles the chat by the first 48 characters of its first message (S14), so the list asks for nothing else. Never "New chat", "Untitled chat" or "never".
- **Related chats** (D42, SPEC §14.10): a chat hangs from its root, else its parent, one level deep, so a clean chat of a clean chat sits under the first chat; if that chat was deleted it hangs from the oldest chat of its line still listed (never from two rows at once), else it stands alone. One whose anchor holds a schedule's result joins that schedule's series, headed by the newest result; earlier results read as their date ("2 h ago", "12 Oct", full title in the tooltip) and a chat continued from one of them by its title. `GET /api/sessions` sends the anchors of a page's chats that aren't on it as `parents`, so a family is drawn before its head's page loads.
- **Order and paging:** a family sits where its newest message puts it, in that day's group. "Show more" counts rows, 30 a press; a family is one row. The first load reads up to three pages of 50 until there are 30 rows.
- **Disclosure:** a head with related chats gets a trailing toggle after its title (`IconButton` "chevron-right", small, quiet, turned 90° when open; 24 × 32 on fine pointers, 44 × 44 on coarse ones, always visible), so a row reads link, toggle, state or ⋯. On a head the ⋯ keeps its room while hidden and a state glyph is as wide as the ⋯, so the toggle never moves when the pointer reaches the row. It is a `<button aria-expanded aria-controls>` named "Related chats for “{title}”", with no count (counts change while paging). The related chats are a nested `<ul aria-label="Related to “{title}”">` in the head's `<li>`, hung on a guide line (`ml-3 border-l border-border-strong pl-2`), rows as tall as any other, each with its own ⋯. Families start closed; the toggles are remembered in `localStorage["bothy.sidebar.open"]` (a JSON array of at most 100 keys; storage that fails is ignored). The family of the open chat opens by itself, once, without being remembered, and never closes by itself; a head whose family is closed with the open chat in it is raised like the open chat.
- **State of a closed family:** the strongest of its chats, the waiting dot over the spinner, with hidden words "Waiting for approval in a related chat" or "Bothy is working in a related chat" when it isn't the head's own.
- **Row menu:** Rename and Delete chat…, as for any row; New related chat is in the chat's own menu (§7.1). Search (⌘K) still lists chats one by one.
- Sidebar scroll position is kept across navigation.
- **Account menu** (`Menu`, opens upward): "Your account", "Theme ▸" (System, Light, Dark as `menuitemradio`), "Keyboard shortcuts", separator, "Pause Bothy…" (admins, `danger-text`), "Sign out".

### 4.3 Settings navigation

Opening Settings swaps the sidebar's contents (same width and colour, so nothing jumps). There is never a second navigation column.

```
┌──────────────────────────┐
│ ← Back to Bothy          │  returns to the last non-settings route, or /chat
│                          │
│ You                      │  text-caption muted
│ ⚇ Account                │  user
│                          │
│ Workspace                │  text-caption muted
│ ≡ General                │  sliders
│ ▣ Models                 │  models
│ ⏚ Connections          • │  plug; attention dot for a disconnected app or a tool to review
│ ⚇ People                 │  users
│ ⛉ Security             • │  shield; attention dot when there are findings
│ ‹› Advanced              │  code
└──────────────────────────┘
```

Rows use the sidebar's nav-row recipe and icon rule (§4.2): 18 px outline icon, muted, the current group raised with its icon in moss. The groups and their icons are `shell/settings.ts`.

Members have no "Settings" item in the sidebar. Their account menu opens `/settings/account`, whose settings sidebar shows only "Account".

On phones and tablets `/settings` is a list page of the same groups, each row led by the same icon; each group page has "← Settings" as its back link.

### 4.4 Phone and tablet top bar (< 1024 px)

```
360 px
┌────────────────────────────────────┐
│ [≡•] Fix login redirect          ⋯ │  52 px, bg-bg; border-b only after scroll
└────────────────────────────────────┘
```

- `[≡]` opens the sidebar as a drawer from the left: `min(320px, 85vw)`, full height, `shadow-overlay`, scrim behind. It closes on scrim tap, Escape or navigation. An 8 px ember dot on the button means approvals are waiting (hidden text "{n} approvals waiting").
- Title: `text-heading`, truncate. On pages it is the page title, shown once the page's own heading has scrolled under the bar (or under a banner, §4.9); in a chat the chat title. Renaming a chat happens in this slot, on the same row: the field takes the title's place (`flex-1 min-w-0`, `h-11`), and no second bar opens over the transcript.
- "{n} waiting": a small ghost text button in `warning-text` (ember: it needs this person), shown only when something waits for them; opens `/approvals`. Under 640 px it is hidden on chat routes, where the chat title needs the room: the dot on `[≡]` and the Approvals count in the drawer still say it. A bare count without the word would be ambiguous, so the link is hidden rather than shortened.
- Right slot by page: chat `⋯` (chat menu); Knowledge project "Edit"; elsewhere nothing.
- The red power icon and the bell from v0.1 (`20b`) are removed.

### 4.5 Routes

| Route | Screen |
|---|---|
| `/setup` | Setup (§5) |
| `/login` | Sign in |
| `/password` | Choose a new password (first sign-in after an admin created the account) |
| `/` | Redirect to `/chat` |
| `/chat` | A new, unsaved chat |
| `/chat/:id` | A chat |
| `/approvals`, `/approvals/:id` | Approvals inbox; `:id` opens that item expanded in the same list |
| `/knowledge` | Knowledge → Projects |
| `/knowledge/search` | Knowledge search (`?q=`, `?repo=`) |
| `/knowledge/memory` | Knowledge → Memory |
| `/knowledge/skills`, `/knowledge/skills/:name` | Skills list, one skill |
| `/knowledge/projects/:owner/:repo` | Project notes |
| `/activity` | Activity → Tasks |
| `/activity/tasks/:id` | Task detail |
| `/activity/schedules` | Schedules |
| `/activity/schedules/:id` | Schedule form; `:id` = `new` creates, `checkin` opens the check-in form |
| `/activity/releases` | Releases |
| `/activity/spending` | Spending |
| `/activity/audit` | Audit log (admins) |
| `/settings` | Group list (phones) or redirect to `/settings/general` (admins) / `/settings/account` (members) |
| `/settings/:group` | `account`, `general`, `models`, `connections`, `people`, `security`, `advanced` |
| `/settings/:group/:item` | `general/personality`; `account/keys` (everyone); `connections/{slack,discord,telegram,github,search,mcp-<name>}`; `security/{permissions,websites,keys,google-sign-in,github-sign-in}` |

Patterns never overlap: owners of repositories can be called anything, so project notes live under `/knowledge/projects/`.

Redirects (with `history.replaceState`) so links already sent to chat apps and bookmarks keep working:

| Old | New |
|---|---|
| `/runs/:id` | `/activity/tasks/:id` |
| `/schedules`, `/schedules/:id` | `/activity/schedules`, `/activity/schedules/:id` |
| `/memory` | `/knowledge/memory` |
| `/usage` | `/activity/spending` |
| `/audit` | `/activity/audit` |
| `/settings/channels`, `/settings/mcp` | `/settings/connections` |
| `/settings/policy`, `/settings/sandbox` | `/settings/security` |
| `/settings/vault` | `/settings/security/keys` |
| `/settings/tokens` | `/settings/account` |
| `/settings/persona`, `/settings/budgets` | `/settings/general` |
| `/settings/skills` | `/knowledge/skills` |

`/approvals/:id` is unchanged because `Card.WebURL` already points there.

### 4.6 What moves where

| v0.1 | v0.2 | Main flow, Details or Advanced |
|---|---|---|
| Rail: Chat; second chat column with its own "New chat" | Sidebar: New chat, Search, chat list | Main |
| Rail: Approvals, mobile bell | Sidebar: Approvals with a count; phone "{n} waiting" | Main |
| Rail: Schedules | Activity → Schedules | Main |
| Rail: Memory (MEMORY.md, USER.md, HEARTBEAT.md) | Knowledge → Memory ("About you", "Things Bothy remembers"); HEARTBEAT.md becomes the Check-in checklist in Activity → Schedules | Main |
| Rail: Usage | Activity → Spending, in money; token columns behind "Technical details" | Main + Details |
| Rail: Audit (+ network tab) | Activity → Audit log with "Actions" and "Website access" views; raw JSON per row behind "Raw record" | Main + Details |
| Run trace `/runs/:id` | Activity → task detail; the trace behind "Technical details" | Main + Details |
| Top bar: spend meter | Activity → Spending; banner only when the daily limit stops work | Main |
| Top bar: sandbox chip; yellow banner | Settings → Security → Running code; an attention item there; an inline chat notice when something needs code | Main (Security) |
| Top bar: Stop all; Advanced: kill switch; confirm dialog | Account menu → "Pause Bothy…"; status and Resume in Security; the per-chat Stop stays in the composer | Main |
| Chat header: P·U·S badge and dialog | One notice in the transcript when a chat becomes exposed; a warning line on later cards | Main |
| Chat header: model dropdown | Composer model picker | Main |
| Chat header: pin counter | Chat menu → Pinned | Main |
| Chat header: context ring, breakdown drawer | Chat menu → Chat details → "Technical details" | Details |
| Tool rows: verdict chip, duration, rule, raw Action JSON | Activity line → Details | Details |
| `tool_search`, `use_tool` rows | Never rendered; the tool they reach is shown instead | Removed |
| Card TOOL/PATH fields, raw diff, session id | Question, plain fields, diff behind "Show"; tool, rule and ids behind Details | Main + Details |
| Settings → General: mode | Removed; team mode is on from setup | Advanced |
| Settings → General: timezone, public URL, remote-access status | General → Workspace | Main |
| Settings → General: extra host names | Advanced → Network | Advanced |
| Settings → Models & providers: provider cards | Models → Providers rows, Manage dialog | Main |
| Settings → Models: slot chains | Models → Main model, Quick tasks model; "Backup models" disclosure | Main + disclosure |
| Settings → Models: pricing overrides | Advanced → Model prices | Advanced |
| Test telemetry (context, latency, test cost) | Models → provider → Manage → Details | Details |
| Settings → Chat apps | Connections → each app's page (who can use Bothy there, group chats); people in chat apps in People → In chat apps; people waiting to be let in and new group chats arrive as Approvals items. Linking chat accounts to Bothy accounts is gone. | Main |
| Settings → People | People | Main |
| Settings → Policy: preset, timeouts | Security → Approvals | Main |
| Settings → Policy: grants list | Security → "Allowed without asking" page | Main |
| Settings → Policy: rules table, "what would happen" tester, class overrides | Advanced → Rules | Advanced |
| Settings → Sandbox & egress: status | Security → Running code | Main |
| Settings → Sandbox & egress: allowlist, presets, recent blocks | Security → "Websites Bothy can reach" page; package downloads switches; recent blocks in Audit log → Website access | Main |
| Settings → Sandbox & egress: backend, runtime, limits, quotas, active boxes, shared-tenant suffixes | Advanced → Running code, Network | Advanced |
| Settings → MCP servers, tool review | Connections → Tools; reviews arrive in Approvals | Main |
| Settings → Skills | Knowledge → Skills | Main |
| Settings → Persona: name, tone, standing rules, SOUL.md | General → Bothy (name, tone, standing instructions, "Personality" document) | Main |
| Settings → Persona: constitution | Advanced → Built-in safety rules (read-only) | Advanced |
| Settings → Vault | Security → Saved keys; KEK colocation as an attention item; unlock on a blocking screen and banner | Main |
| Settings → API tokens | Account → API access | Main |
| Settings → Budgets: global cap | General → Spending → Daily limit | Main |
| Settings → Budgets: per-person caps | People → person → Spending limit | Main |
| Settings → Budgets: per-run, scheduled, heartbeat caps; rate limits | Advanced → Limits | Advanced |
| Settings → Advanced: effective config, doctor, backup, log level | Advanced | Advanced |
| Setup: Safety, Chat apps, Make it yours | Defaults (§5.8), editable in Settings | Main (Settings) |
| Setup: Done security check | Security → Needs attention; attention dot | Main (Security) |
| Banners for MCP pins and changed skills | Approvals items for admins | Main |
| Notices for people waiting to be let in and new group chats | Approvals items | Main |

### 4.7 Behind "Details" or in Advanced

Never shown by default, always reachable:

- Tool names (`fs_delete`, `mcp__linear__create_issue`), rule ids (`default:local_write`), verdicts, side-effect classes.
- Session, run, approval, user and schedule ids (Details shows them with Copy).
- Token counts, context window, cache hits, time to first word, step counts, per-step latency.
- Raw provider, network and Go error strings.
- Model ids (`us.anthropic.claude-sonnet-4-5-20250929-v1:0`); the UI shows "Claude Sonnet 4.5".
- Cron expressions, except when the person picks "Custom" in a schedule.
- Config keys, provenance (UI, env, file), effective YAML, env-var names.
- Egress log internals, sandbox runtime, engine, limits and quotas.
- The constitution.
- Byte counters on notes (shown only above 80 % of the cap, as a sentence).
- Refine drafts, critiques, corrections and the vote.

**Details** is a disclosure on the same screen ("Details", "Show", "Technical details", "Raw record"), closed by default. **Advanced** is the last settings group, admin-only, and the only place config keys, provenance and env-var names appear.

Never behind Details, on any approval card: the full command, the full outgoing message, the recipient, the website, the repository and branch, the environment and version. "Show" only folds diffs and file contents.

### 4.8 Removed outright

- The global top bar and everything in it; the permanent sandbox banner and the duplicate chip.
- The second chat column and its "New chat" button.
- The P·U·S badge, the context ring, the pin counter, the header model dropdown.
- "Used use_tool" rows.
- Setup steps Safety, Chat apps and Make it yours; progress dots; "Step 3 of 7 · Models"; the Main/Fast chain editors, Test button, "Use this provider" button, "Private address" toggle and typed "Model to test" field; the password strength bar; the household/team toggle; the security audit on Done.
- `default` and provenance chips outside Advanced; section subtitles that only reassure; icons on nav items; icon tiles (the logo aside).
- KPI card rows, the 11-colour legend, backdrop blur, keyboard chips on buttons.
- "ok" pills on audit rows; the duplicate card on the approval detail page.
- Empty chats in the list; the timestamp "never"; "Untitled chat".
- The composer helper line "Enter to send · Shift+Enter for a new line" and "(/ for commands)".
- The theme switcher strip in the sidebar footer (theme is in the account menu and Account).
- Every personal-assistant phrase ("Household", "your own assistant", "your machine", "plan a calm week").

### 4.9 Banners

A banner sits at the top of the main area (not over the sidebar), full main width, one at a time, highest priority first. `min-h-10 px-6 py-2 text-body`, one sentence, at most one action. It cannot be dismissed while the condition stops work; otherwise it can be dismissed for the session. It is sticky (under the phone top bar), so a chat that opens at its latest reply still shows it, and the chat header sticks below it; the chat fills the window under it, so a short chat never scrolls. The page's scroll padding clears the banner and the phone top bar, so a request opened from its link (§8.8) or a control reached with the keyboard is never hidden under them.

| Condition (SPEC `banner` event) | Who | Tone | Copy | Action |
|---|---|---|---|---|
| Paused | everyone | warning | "Sam paused Bothy at 12:15 AM. Nothing new starts until an admin resumes it." (another day "… on Oct 12 at 10:42."; who unknown "Bothy was paused at 12:15 AM."; nothing known "Bothy is paused.") | Admins: "Resume" |
| Vault sealed | admins | danger | "Bothy's saved keys are locked, so it can't reach its models or connections." | "Unlock" |
| Provider auth failed | admins | danger | "{Provider} stopped accepting Bothy's key. Chats will fail until it's replaced." | "Update key" |
| Config errors / safe mode | admins | danger | "Bothy started with safe defaults because some settings couldn't be loaded." | "See what's wrong" (Advanced) |
| Daily spending limit reached | admins | warning | "Bothy reached today's $5.00 spending limit. New work waits until midnight." Not shown on Activity → Spending, which says it under the meter. | "Change limit" |
| Restart required | admins | neutral | "Restart Bothy to apply your changes." | "Open Advanced", where the Restart Bothy notice lists what needs it |

`unknown_sender` (someone is waiting to be let in) and `new_channel` (Bothy was added to a group chat) only refresh the Approvals inbox, which lists them.

Not banners any more: sandbox off or not isolated (Security attention item), MCP tools or skills pending review (Approvals items and the attention dot), spending at 80 % (Spending page line), chat apps at 80 % of their share, `chat_budget_80` (Spending page line, §10.5), and people in chat apps moved off linked accounts, `team_access` (a notice on People → In chat apps and above the Approvals list, §11.5, §8.8): neither stops work. Members see only "Paused"; a member who hits the daily limit sees it as a chat notice.

### 4.10 The attention dot

An 8 px `rounded-full bg-warning` (ember) dot with visually hidden text "Needs attention" (`Dot`; 3.6:1 on the canvas, 3.1:1 on a selected row), after "Settings" in the sidebar and after the group name inside Settings (Security for findings and sandbox problems, Connections for a disconnected app or a tool needing review). It looks again a second after a chat app, tool, sandbox, setting or banner changes (a banner clearing, such as the one for actions waiting for review, is often the only sign), so it goes without a reload. Besides it there are only the chat-row waiting dot and the 6 px dot beside the Approvals count.

### 4.11 Who sees what

| Place | Owner, Admin | Member |
|---|---|---|
| Chat, Search | their own chats | their own chats |
| Approvals | what they can decide and what they asked for | same |
| Knowledge → Projects, project notes | read, edit notes, approve suggestions, read again, stop reading a repository | read, edit notes (every change is in History), approve suggestions they are eligible for; no "Read again" or removal |
| Knowledge → Memory | their own | their own |
| Knowledge → Skills | add, turn on and off, review changes | read |
| Activity → Tasks, Spending | everyone's, with a person filter | their own (the heading still reads "Activity") |
| Activity → Schedules | all, plus "In chat apps": the schedules people and channels set up there, to turn off or delete | their own |
| Activity → Releases | yes | yes, read-only |
| Activity → Audit log | yes | hidden |
| Settings → Account | yes | yes (their own saved keys included) |
| Settings → workspace groups | yes | hidden |
| Account menu → Pause Bothy | yes | hidden |
| Banners | all that apply | "Paused" only |

Owner and Admin differ only in that the owner cannot be removed or demoted.

People in chat apps and channels have no account. Their chats, tasks, files and notes are theirs and are not on the web, for admins either (SPEC §5.2); admins see what they spend (Spending), their schedules (Activity → Schedules), their requests (Approvals) and what Bothy read for them outside its own files (Audit log).

---

## 5. Onboarding

### 5.1 Flow

```
open the setup link (#t=… in the URL)
   │
   ├─ no token, or token rejected ─────► Setup code (a gate, not a step)          5.2
   ├─ opened from another computer ────► Finish setup on the server               5.3
   ▼
Step 1  Create your account   POST /api/setup/owner {display_name, username, password}
                              then PATCH /api/config {mode: multi, timezone from Intl}, silently
   ▼
Step 2  Connect an AI model   POST /api/providers/detect on entry
                              POST /api/providers/test automatically on select / paste
                              Continue: store key → write provider → set main and quick-tasks models
   ▼
Step 3  Bothy is ready        POST /api/setup/complete on entry
                              POST /api/security-audit {fix:false} in the background
   ▼
/chat, or the Settings page chosen in a "Set up" row
```

State comes from the server, so a reload resumes at the right step. Setup already finished: `/setup` redirects to `/chat` with a session, `/login` without. Owner exists but setup is unfinished and there is no session: `/login?next=/setup` with the notice "Sign in to finish setting up Bothy."

Target: with the setup link and a detected AWS or Ollama option, the first chat is **3 screens, 3 typed values (name, username, password) and 3 presses** (Create account, Continue, Start chatting). With a pasted key it is 4 typed values.

### 5.2 Frame

```
                     |<------------------- 480 px ------------------->|
  15vh top margin (24 px on phones)
                     ▣ Bothy                               Step 1 of 3     28 px mascot, wordmark text-heading · text-small muted
                                                                           (40 px)
                     Create your account                                   text-title (28 px)
                     You'll be the owner of this Bothy. You can add        text-reading muted, mt-2
                     your team when you're done.
                                                                           (32 px)
                     (fields, 20 px apart)
                                                                           (32 px)
                     [ Create account ]                                    primary, h-10, left-aligned
```

- Setup steps 1 to 3 (above): no card, no border, no progress bar, no icon besides the logo; page background is `bg`, because step 2's option list is a card of its own. The intro is `text-pretty`, so its last line never holds one word alone.
- **The card frame** (`Frame card`), for screens that are one short form: sign in, choose a new password, the setup code and finish setup on the server. On the canvas (`bg-sidebar`) at ≥ 640 px, a 424 px card (`rounded-xl border border-border bg-surface p-9 shadow-pop`) 10 vh from the top holds the 56 px logo centred (a 1 px `border-strong` ring in dark), the `text-title` centred, the intro centred, then the form with 44 px fields. An optional `after` slot sits under the card, centred, `text-small text-muted`. Under 640 px there is no card: the same column on `bg`, 24 px from the top.
- Buttons are left-aligned on setup steps and full width under 640 px; in the card frame the primary is full width and 44 px tall.
- No Back on steps 2 and 3 (the account already exists).
- Errors appear inline under the field or the button, never as a toast. On an error, focus moves to the first field that has one.
- Pressing Enter in any field submits the step.

### 5.3 Setup code (gate)

Shown only when the URL has no `#t=` token or the token was rejected. No "Step N of 3".

```
Bothy

Set up Bothy
Paste the setup code Bothy printed when it started.

Setup code
[                                                  ]

[ Continue ]

Lost the code? Run this on the server to see it again:
┌──────────────────────────────────────────────────────┐
│ docker compose exec bothy /bothy setup-token  [Copy] │   CommandBlock: wraps, never truncates
└──────────────────────────────────────────────────────┘
› Not using Docker Compose?
```

- The command depends on `install` from `GET /api/setup/state` (S1): `compose` → `docker compose exec bothy /bothy setup-token`; `docker` → `docker exec bothy /bothy setup-token`; `binary` → `bothy setup-token`; unknown → compose. The disclosure lists the other two, labelled "Docker" and "Installed directly".
- After a valid code the token is removed from the URL (as today) and Step 1 opens.

| Case | Copy (under the field, `danger-text`) |
|---|---|
| Empty | "Paste the setup code first." |
| Wrong or expired | "That code didn't work. It may have expired, so run the command below to get a new one." |
| Too many tries | "Too many tries. Wait a minute, then try again." |
| Server unreachable | "Bothy didn't respond. Check that it's still running, then try again." |

### 5.4 Finish setup on the server

Setup routes answer non-local peers with 403. That gets its own screen with no field:

```
Bothy

Finish setup on the server
For safety, setup only works on the computer Bothy runs on. Run this on your
own computer to connect to it:

┌──────────────────────────────────────────────────────┐
│ ssh -L 8080:127.0.0.1:8080 you@your-server   [Copy]  │   the server's fix line, verbatim
└──────────────────────────────────────────────────────┘
Then open http://localhost:8080/setup in your browser.
```

When the command holds the `<host>` placeholder, the line under it explains it: "Replace `<host>` with your server's address, then open http://localhost:8080/setup in your browser."

A `CommandBlock` always shows its Copy button, and its lines break only between words ("setup-token" never splits); a single word longer than the line still wraps. Copy copies the command byte for byte.

### 5.5 Step 1: Create your account

```
Bothy                                                       Step 1 of 3

Create your account
You'll be the owner of this Bothy. You can add your team when you're done.

Your name
[                                                  ]

Username
[                                                  ]
You'll use this to sign in.

Password                                                           Show
[                                                  ]
At least 12 characters.

[ Create account ]
```

| Field | Type | Validation (on submit, then live once shown) | Error copy |
|---|---|---|---|
| Your name | text, `autocomplete="name"` | required, 1–64 characters | "Enter your name." |
| Username | text, `autocomplete="username"`, `autocapitalize="none"`, `spellcheck="false"`; filled from Your name as it is typed (its first word, lower case, accents dropped: "Morgan Reyes" → "morgan") until the person types a username of their own; Add person does the same | `^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$` | Empty: "Enter a username." With a space: "Usernames can't have spaces. Try {suggestion}." (the typed value lowercased, runs of spaces turned into dots: "Try maya.chen."; only when the suggestion passes the pattern, else the line ends at "spaces."). Anything else: "Use up to 32 letters, numbers, dots, dashes or underscores, starting with a letter or number." |
| Password | password, `autocomplete="new-password"`; "Show"/"Hide" ghost text button at the right of the label row (`aria-pressed`, `aria-controls`) | 12 or more characters; the server rejects common passwords | "Use at least 12 characters." / server: "This password is too common. Try a phrase of a few words." |

Live help under Password (replaces the meter; `aria-live="polite"`, throttled to 500 ms):

| State | Text |
|---|---|
| Empty | "At least 12 characters." |
| 1–11 characters | "{n} more characters needed." ("1 more character needed.") |
| 12 or more | "Long enough." |

- Button: "Create account"; while working "Creating account…" with a spinner, `aria-busy`.
- Silently applied: time zone from `Intl.DateTimeFormat().resolvedOptions().timeZone` (UTC fallback), `mode: multi`, display name from "Your name" (S7).
- Form-level errors (above the button): network "Bothy didn't respond. Check that it's still running, then try again."; setup grant expired "Setup timed out. What you typed is kept." (returns to §5.3, whose intro already asks for the code, and keeps the typed values).

### 5.6 Step 2: Connect an AI model

#### 5.6.1 Layout

```
Bothy                                                       Step 2 of 3

Connect an AI model
Bothy needs a model to think with. Choose one you already have access to.

Found on this server                                       text-small 500 muted; only when something was detected
┌──────────────────────────────────────────────────────┐
│ (•) Use this server's AWS access                     │   OptionList row
│     Amazon Bedrock. No key needed.                   │
│     ──────────────────────────────────────────────── │   expanded content of the selected row
│     Region  [ US East (N. Virginia)            ⌄ ]   │
│     ✓ Connected. Bothy will use Claude Sonnet 4.5.   │
│       Change                                         │
├──────────────────────────────────────────────────────┤
│ ( ) Ollama on this computer                          │
│     3 models ready, including Qwen3 14B.             │
└──────────────────────────────────────────────────────┘

Or choose a provider                                       "Choose a provider" when nothing was detected
┌──────────────────────────────────────────────────────┐
│ ( ) Anthropic          Claude models                 │
│ ( ) OpenAI             GPT models                    │
│ ( ) Google Gemini      Gemini models                 │
│ ( ) OpenRouter         Many models with one key      │
│ ( ) Amazon Bedrock     Uses this server's AWS        │   only when not detected
│                        access, no key needed         │
│ › More providers                                     │   Groq, xAI, DeepSeek, Ollama*, LM Studio*, Other server
└──────────────────────────────────────────────────────┘

[ Continue ]   Connect a model to continue.
```

- Each group is one `OptionList` (§15): a single `border-strong` `rounded-lg` container, rows split by hairlines. The selected row expands inside the same row, indented to the title. Nothing else on the screen has a border.
- Detection runs once on entry; after 400 ms without a result, the "Found on this server" area shows one muted line "Looking for models on this server…".
- Order of detected rows: AWS, Ollama, LM Studio, other local servers, keys found in the server's environment, then AWS settings without access (`problem`).
- If exactly one AWS or local option with access is detected it is pre-selected and its check starts at once, so the person may only need to press Continue. A detected environment key is never pre-selected, because choosing it copies the key into Bothy's saved keys; nor is an AWS row with a `problem`, whose check fails until the person signs in again. When the check of a pre-selected AWS row fails (the access can't use Bedrock, say), the row is deselected and reads "Found, but it can't use Bedrock right now.", so the step never opens on an error about something the person didn't choose; choosing the row shows the reason. A local server found with no models stays selected, since its fix (pull a model) is the next step.
- Only one row is open at a time. Changing the selection cancels a running check (AbortController).
- "More providers" is a disclosure row inside the provider list. Ollama and LM Studio appear there only when not detected.

#### 5.6.2 Options and copy

| Option | Title | Line 2 | Expanded content |
|---|---|---|---|
| AWS detected (S3) | "Use this server's AWS access" | "Amazon Bedrock. No key needed." (the region shows once, in the Region select right below) | Region select (readable names, code as secondary text), default from detection; the check starts on selection and re-runs on region change |
| AWS settings without access (`problem`) | "Use this server's AWS access" | "This server's AWS sign-in has expired." (`signin_expired`) / "AWS settings found, but they don't give access right now." (`no_credentials`) | Listed last among found options and never chosen for the person. Text "Sign in again on the server with `aws sso login`, then check again." (or "Give this server an AWS role or sign in with the AWS CLI, then check again."), secondary button "Check again" (it stays, with the check's progress or failure under it, until a check succeeds), link "How to give Bothy AWS access ↗"; a successful check turns it into the AWS detected row |
| Ollama detected | "Ollama on this computer" | "{n} models ready, including {best model}." / "Running, but no models downloaded yet." | the check, then the success line |
| LM Studio detected | "LM Studio on this computer" | "{n} models ready." | same |
| Other local server detected | "Model server on this computer" | "{n} models available." | same |
| Environment key detected | "Use the {Provider} key on this server" | "Found in the server's settings. Choosing it saves a copy in Bothy's saved keys." | the check; Continue imports it (`POST /api/vault/import`) |
| Anthropic, OpenAI, Google Gemini, OpenRouter | provider name | "Claude models", "GPT models", "Gemini models", "Many models with one key" | `KeyField` "API key" (password style), help "Stored encrypted. Only sent to {Provider}." with the link "Get a key at {console} ↗" on its own line under it, so the link never splits |
| Groq, xAI, DeepSeek (More providers) | provider name | "Fast open models", "Grok models", "DeepSeek models" | same |
| Amazon Bedrock (not detected) | "Amazon Bedrock" | "Uses this server's AWS access, no key needed" (the same line in Settings → Models → Add a provider) | Region select, then secondary button "Check access" and link "How to give Bothy AWS access ↗" (`docs/deploy/`). No paragraph: line 2 already says it |
| Ollama, LM Studio (not detected) | "Ollama" / "LM Studio" | "On this computer or your network" | "Server address", default `http://host.docker.internal:11434` / `http://host.docker.internal:1234/v1`; checked 800 ms after editing stops |
| Other server | "Other server" | "Any OpenAI-compatible server" | "Server address" (placeholder `https://models.example.com/v1`), "API key (if needed)"; after the check, the model comes from the server's list; only if the list is empty does a "Model name" text field appear. An address failure (`private_address`, `unreachable`, `tls`, `not_a_model_server`, `no_models`) shows right under "Server address", before the key field; only `key_rejected` shows under the key |

Key console links: Anthropic `console.anthropic.com`, OpenAI `platform.openai.com/api-keys`, Google Gemini `aistudio.google.com`, OpenRouter `openrouter.ai/keys`, Groq `console.groq.com/keys`, xAI `console.x.ai`, DeepSeek `platform.deepseek.com`.

#### 5.6.3 The check

- Starts on paste, on Enter, or 800 ms after typing stops once the value is plausibly long (20 characters for keys). There is no Test button.
- Checking line (spinner + `text-small muted`): "Checking the key…", "Checking AWS access…", "Checking which models you can use…" (Bedrock listing), "Checking…" (address).
- Success: check glyph in `accent-text` + "Connected. Bothy will use {Model name}." in ink, then a "Change" link button. "Change" swaps the sentence for a "Model" select of the provider's models by friendly name (S5), recommended first with " (recommended)", which is never added to a model the check refused. The quick-tasks model is chosen silently (the provider's recommended cheap model, or the same model). "Main", "Fast" and "fallback" are never mentioned here.
- Model notes under the success line (`text-small muted`, except the last row), replacing "compact mode", "degraded", "text-only" and every context number:

| Probe result | Note |
|---|---|
| 32K window or more | none |
| 16K to under 32K | "This model keeps less of long conversations in mind. Bothy summarises older messages to keep going." |
| 8K to under 16K | "This model can only keep short conversations in mind, so Bothy may forget earlier messages. A larger model works better." |
| Under 8K, or no tool use | A warning `StatusLine` (amber alert glyph, `warning-text`), not fine print: "{Model} can only chat. Choose a larger model to let Bothy read files or search the web." Continue still allowed. |

#### 5.6.4 Failures

Every failure is a `StatusLine` in danger tone: alert glyph, one or two sentences in `danger-text`, an optional action, then a "Details" disclosure (unindented under 640 px, so a code block keeps the width it needs) holding the raw server message in a `CodeBlock` with Copy. For AWS, Details also names the credential source, never its value ("Credentials from: instance role bothy-prod"). Category comes from the server (S13); until it does, the client maps HTTP status and known substrings. The sentence never repeats its action: "Try again" and "Check again" are the link right after it, so the sentence stops before saying them.

| Category | Main line | Action |
|---|---|---|
| `key_rejected` | "That key wasn't accepted. Check you copied all of it, or create a new one." | Link "Get a new key ↗" |
| `no_model_access` | "This key works, but it can't use {Model}. Choose another model." | Model select opens |
| `out_of_credit` | "Your {Provider} account is out of credit. Add credit with {Provider}." | "Try again" |
| `rate_limited` | "{Provider} is limiting requests right now. Wait a minute." | "Try again" |
| `provider_error` | "{Provider} isn't responding properly right now." | "Try again" |
| `timeout` | "{Provider} took too long to answer." | "Try again" |
| `unreachable` (cloud) | "Bothy couldn't reach {Provider} from this server. Check the server's internet connection or firewall." | "Try again" |
| `unreachable` (local) | "{Name} isn't answering at that address. Make sure it's running." | "Try again"; Details adds "If Bothy runs in Docker, {Name} must accept connections from other computers (for Ollama: OLLAMA_HOST=0.0.0.0) and the address is usually http://host.docker.internal:{port}." |
| `private_address` | "This server is on your private network. Allow Bothy to reach it?" | Secondary "Allow {host:port}" (sets `allow_private` for that exact host and port, then re-checks) |
| `no_models` | "{Name} is running but has no models yet. Install one on the server, for example `ollama pull qwen3:8b`." (the example only for Ollama). On the detected Ollama row, whose line 2 already says "Running, but no models downloaded yet.", only "Install a model on the server, for example `ollama pull qwen3:8b`." | "Check again" |
| `tls` | "The connection to that address isn't secure because of a certificate problem. Check the address. For a server on your own network you can use http://." | none |
| `not_a_model_server` | "That address answered, but it doesn't look like a model server. Check the address; it usually ends in /v1." Ollama, whose API has no /v1: "That address answered, but it isn't Ollama. Check the address and port; Ollama usually uses 11434." | none |
| `aws_no_credentials` | "This server has no AWS access right now. Give it an AWS role, or sign in with the AWS CLI." For a key read from AWS Secrets Manager: "Bothy couldn't read this key from AWS Secrets Manager because this server has no AWS access right now. Give it a role that can read the secret, or sign in with the AWS CLI." | Link "How to give Bothy AWS access ↗" |
| `aws_denied` | "This server's AWS access isn't allowed to use Bedrock. Ask your AWS admin to allow it." | Details names where the access comes from ("Credentials from: {source}") and lists `bedrock:InvokeModelWithResponseStream`, `bedrock:ListFoundationModels`, `bedrock:ListInferenceProfiles` |
| `aws_no_models` | "No Claude or Nova models are turned on for this AWS account in {Region name}. Turn on model access in the Bedrock console, or choose another region." | Link "Open the Bedrock console ↗" |
| `unknown` | "Something went wrong while checking. You can also choose another option." | "Try again" |

#### 5.6.5 Continue

- Until a selected option has connected, Continue is `aria-disabled="true"` (focusable, styled disabled) with "Connect a model to continue." beside it in `text-small muted`, linked by `aria-describedby`. Pressing it moves focus to the selected row, or the first row.
- Label "Continue"; while saving "Saving…". One press stores the key in the vault (or imports the environment key), writes the provider (with `allow_private` for a detected or explicitly allowed local address), sets the main and quick-tasks models, and moves to Step 3. Until S6 lands the client runs the existing calls in sequence.
- Save failed: "Bothy couldn't save this connection. Try again. If it keeps happening, check that the server's data folder isn't full or read-only." under the button.

### 5.7 Step 3: Bothy is ready

```
Bothy                                                       Step 3 of 3

Bothy is ready
Start a chat now, or connect more. Everything here is also in Settings.

Connect a chat app                                                      ›
Your team can talk to Bothy in Slack, Discord or Telegram.
──────────────────────────────────────────────────────────────────────────
Connect GitHub                                                          ›
Let Bothy read your code and open pull requests for review.
──────────────────────────────────────────────────────────────────────────
Add people                                                              ›
Give each person their own sign-in to Bothy.

[ Start chatting ]
```

- Setup is marked complete when this screen opens, so each row goes to a normal app page: `/settings/connections`, `/settings/connections/github`, `/settings/people` with the "Add person" dialog open.
- Rows are link rows: the title (`text-body` 500) is the link and its hit area covers the row, a muted › sits at the right edge, and the description (`text-body` muted, `text-pretty`) stays under the title. No row has a button at any width, so "Start chatting" is the only button on the screen. "Add people", not "Invite", because Bothy creates the accounts and sends no invitations.
- "Start chatting" is the one primary and opens `/chat`, where the owner's welcome chat opens in place of the empty chat (§7.2).
- No warnings and no icons. The background security audit writes to Security → Needs attention and lights the attention dot.

### 5.8 Defaults applied without asking

| Setting | Default | Where to change it |
|---|---|---|
| Team mode | On (`mode: multi`) | not shown (Advanced → Configuration file) |
| Time zone | The browser's at setup, for you and as new people's default | Settings → Account (yours), Settings → General → Workspace (new people's) |
| When Bothy asks (preset) | Careful (unchanged config default) | Settings → Security → Approvals |
| If nobody answers | Chats: deny after 15 minutes; schedules: wait up to 24 hours | Settings → Security → Approvals |
| Daily spending limit | $5.00 for everyone | Settings → General → Spending |
| Per-person limit | none | Settings → People |
| Web search | SearXNG if reachable at setup, otherwise off | Settings → Connections → Web search |
| Package downloads ("Let code download Python packages", "… JavaScript packages") | Off | Settings → Security → Running code |
| Bothy's name and tone | "Bothy", "Warm and brief" | Settings → General → Bothy |
| About you | Empty | Knowledge → Memory |
| Check-in | Off; the form suggests lines about the repository while the list is empty | Activity → Schedules → Check-in |
| Schedules | None; up to three examples under the list until the person has three (D45) | Activity → Schedules |
| Welcome chat | One per new account, opened the first time the app loads (D43) | Delete it like any chat |
| Starters | Up to four rows under the empty chat and the welcome message, from what is set up (D44) | — |
| Quick tasks model | The provider's recommended cheap model | Settings → Models |
| Backup models | None | Settings → Models → Backup models |
| Refine | Off | Composer; default in Settings → Models |

### 5.9 Sign in and first password

```
canvas
            ┌──────────────── 424 ────────────────┐
            │                 ▣                   │  56 px logo
            │          Sign in to Bothy           │  text-title, centred; no subtitle
            │                                     │
            │ [ G  Continue with Google        ]  │  secondary, 44 px, full width, official mark 18 px
            │ [ ◉  Continue with GitHub        ]  │  only providers an admin turned on
            │ ───── or sign in with a password ── │  hairlines + text-small muted; only when a provider shows
            │ Username                            │
            │ [                                 ] │  44 px
            │ Password                            │
            │ [                              👁 ] │  eye toggle inside the field
            │ [            Sign in              ] │  primary, 44 px, full width
            └─────────────────────────────────────┘
                 Forgot your password? Ask an admin.     text-small muted
                          bothy.acme.com                 the address, 24 px below
```

The page uses the card frame (§5.2). The provider buttons (SPEC §15) carry the official Google "G" and GitHub mark (D48), never recoloured beyond GitHub following the text colour; while one is opening its mark gives way to the spinner. There is no "Forgot it?" link and no invite line: Bothy has no reset flow and no invites (D21), so the line under the card sends people to an admin. The password field's eye (`eye` / `eye-off`, quiet small icon button inside the field's end) is named "Show password" or "Hide password" for what it does next and controls the field (`aria-controls`); the same field is used for setup's account step and choosing a new password. A click on a provider asks the server to start (`POST /api/auth/oauth/{p}/start` with `next`) and opens the provider's page; while it leaves the button reads "Opening Google…". The provider sends the person back through the server, which signs them in and lands on `next`, or returns to `/login?signin_error=…&provider=…`. The reason shows once, in the alert slot above the buttons, and the address is cleaned (`next` kept) so a reload doesn't repeat it. Password sign-in is always there.

| Case | Copy |
|---|---|
| Wrong username or password | "That username and password don't match." (form-level, above the button) |
| Locked out | "Too many tries. Try again in {n} minutes." |
| Account turned off | "This account is turned off. Ask an admin to turn it back on." |
| Signed out by expiry | Notice above the form: "You were signed out. Sign in again to continue." |
| Network | "Bothy didn't respond. Check your connection, then try again." |
| Provider: cancelled | "Signing in with {Google} was cancelled." |
| Provider: expired | "That sign-in took too long or was started in another tab. Try again." |
| Provider: not on the list | "{pat@gmail.com / @octo} isn't on this Bothy's sign-in list. Ask an admin to add you." The account comes from the server's refusal cookie (60 s, read once and deleted, never the address, so a crafted link can't name anyone) and shows only when it looks like an address or a login, else "That {Google} account isn't…". The list itself is never shown. |
| Provider: Google hasn't verified the address | "Google hasn't verified {pat@gmail.com} yet. Verify it in your Google account, then try again." Shown for every unverified address, listed or not, so it says nothing about the list; the account comes from the refusal cookie as above, else "that address". |
| Provider: GitHub organization restricts Bothy | "Your GitHub organization hasn't approved Bothy yet. Ask an organization owner to approve it, then try again." |
| Provider: on the list, no account yet | "No Bothy account uses that {Google} account yet. Sign in with your password and connect it in Account, or ask an admin." |
| Provider: account turned off | "This account is turned off. Ask an admin to turn it back on." |
| Provider: anything else | "{Google} sign-in isn't working right now. Sign in with your username and password, or ask an admin." |
| Opened at another address | "Open Bothy at {bothy.acme.com} to sign in with {Google}." + link button "Go there" |

The first time a new account reaches the app (after setup's "Start chatting", the first sign-in or the first password change) the shell asks for its welcome chat once (`Me.welcome_pending`, `POST /api/me/welcome`) and, on `/chat` without `?prompt`, opens it in place of the empty chat. Accounts from before 0015 never get one.

**Choose a new password** (`/password`, after signing in with a one-time password, S9): title "Choose a new password", sentence "You signed in with a one-time password. Choose your own to continue.", one password field with the Step 1 help text, button "Save and continue". Typing the one-time password again as the new one: "Choose a new password, not the one-time password." on the field. Locked after too many wrong one-time passwords (asked again after a reload): "Too many tries. Try again in {n} minutes." under the form.

---

## 6. App shell and page frame

### 6.1 Desktop (≥ 1024 px)

```
┌ canvas ───────────────────────────────────────────────────────────────────────────────────┐
│ ▣ Bothy              ╭ panel, inset 8 px, 14 px corners ──────────────────────────────╮   │
│ ✎ New chat           │ (banner, full panel width, only when §4.9 applies)             │   │
│ ⌕ Search        ⌘K   │         ┌──────────── column 768 (720 content) ────────────┐   │   │
│ ☑ Approvals     • 2  │         │ ← Knowledge                     text-small muted │   │   │
│ ▯ Knowledge          │         │ acme/web                      [ Edit ]  ⋯        │   │   │
│ ↺ Activity           │         │ (page content)                                   │   │   │
│ …                    │         └──────────────────────────────────────────────────┘   │   │
│                      ╰────────────────────────────────────────────────────────────────╯   │
└────────────────────────────────────────────────────────────────────────────────────────────┘
```

- No app-level header. The document scrolls; the sidebar is sticky with its own scroll.
- **The inset panel** (D3, D46): the page is `bg` and the sidebar `bg-sidebar`; a fixed overlay after the layout (`pointer-events-none fixed inset-y-2 right-2 left-66 z-30 rounded-lg shadow-frame`, `aria-hidden`, `data-frame`) draws the panel's edge and paints the canvas around it with a 100vmax spread. Nothing else moves: the document, the sticky chat header and composer, and the scroll logic are as before; `--inset` (8 px at ≥ 1024 px, 0 below) keeps sticky things inside the frame (`top-(--inset)`, `bottom-(--inset)`, `scroll-padding-top`). The panel spans x 264–1272 at 1280, so the column centre and the content edge (x = 408) are unchanged. The sidebar is `z-40` above the overlay; menus, toasts and the skip link stay at `z-50`, dialogs and drawers in the top layer. Full-page screenshots hide `[data-frame]` (§18.2 G3).
- Every page is `Page` + `PageHeader` inside the column. The chat uses the same column (§7.1).
- A "Skip to content" link is the first focusable element; it jumps to `<main>`.

### 6.2 PageHeader

```
[← Back label]                                        text-small muted, 16 px back icon, mb-2, optional
Title                         ✓ Changes saved [actions]   text-title (28 px) as <h1>; status and actions on the title baseline
(Tab  Tab  Tab)                                       optional tabs on a track, mt-4
```

- `flex items-baseline justify-between gap-4`. Actions: at most one primary or secondary button, plus an optional `⋯` menu. Under 640 px actions stay on the right if they fit, otherwise they move under the title.
- No subtitle. Tabs are route links (`aria-current="page"`) on a track (`inline-flex rounded-lg bg-subtle p-1`); the current tab is a raised thumb (`bg-raised shadow-raised rounded-md`), counts as plain muted numbers after the label.
- On settings pages (`saves`, on by default under `/settings/`) a `role="status"` line before the actions says "Changes saved" with a moss check for 2 s after any save on the page (D51, §11.1). Other pages have no status line.
- Content starts 24 px below the title or tabs.

### 6.3 Search dialog

Opened by the sidebar Search item, ⌘K / Ctrl K, or "/" when focus is not in a text field.

```
┌──────────────────────────────────────────────────────────────┐
│ ⌕ Search chats, notes and code                          Esc  │  input text-reading
├──────────────────────────────────────────────────────────────┤
│ Chats                                                        │  text-caption muted
│   v2.3 launch checklist                              Today   │  40 px rows
│   Here's a launch checklist for v2.3. Freeze main on…        │  snippet text-small muted, <mark> on matches
│ Project notes                                                │
│   acme/web · How to release                                  │
│ Code                                                         │
│   acme/web · docs/release.md, lines 10–42                    │
│                                                              │
│ See all results in Knowledge ›                               │
└──────────────────────────────────────────────────────────────┘
```

- 640 px dialog, 15vh from the top, `shadow-overlay`, scrim, no blur; full-screen sheet under 640 px.
- Before typing: "Recent chats" (5 rows). Results: up to 5 chats, 3 notes, 3 code results. Notes and Code groups appear only when Knowledge has a repository.
- Snippets are plain text with `<mark>` (`bg-warning-subtle text-text rounded-sm`) on matches (S15). Chat titles follow the sidebar rule, never "Untitled chat".
- Keyboard: ↑ ↓ move, Enter opens, Esc closes. `role="combobox"` + `listbox`.
- No results: "No results for “{q}”."

### 6.4 Keyboard shortcuts dialog

From the account menu, or "?" when focus is not in a text field. A 440 px dialog listing: ⌘K / Ctrl K Search; / Search; ⌘⇧O / Ctrl Shift O New chat; Esc Stop Bothy (in a running chat) or close; in Approvals: A approve, S allow for this chat, D deny, J / K move, Enter open. Keys render with `Kbd`. This is the only place `Kbd` is used.

---

## 7. Chat

### 7.1 Layout

```
┌ sidebar ┬──────────────────────────────────────────────────────────────────────────┐
│         │        ┌──────────────────── column 768 ─────────────────────┐            │
│         │        │ Q3 pricing notes / Clean up staging notes        ⋯  │  52 px sticky, bg-bg; parent link only for related chats
│         │        │ In #eng on Slack                ← only for chats from apps        │
│         │        ├─────────────────────────────────────────────────────┤            │
│         │        │                   ┌───────────────────────────────┐ │            │
│         │        │                   │ The staging notes are out of  │ │  user message
│         │        │                   │ date. Delete them.            │ │
│         │        │                   └───────────────────────────────┘ │            │
│         │        │ (◉) I'll check what's in the folder first.          │  Bothy's picture starts the turn
│         │        │     ◷ Waiting for your approval                     │  activity line, the card's one status
│         │        │     ┌─────────────────────────────────────────────┐ │            │
│         │        │     │ ▤ Delete notes/staging.md?                  │ │  approval card (§8), type icon
│         │        │     │   This can't be undone.                     │ │
│         │        │     │   [Delete file] [Deny]                      │ │
│         │        │     │   Details                                   │ │
│         │        │     └─────────────────────────────────────────────┘ │            │
│         │        │ ┌─────────────────────────────────────────────────┐ │            │
│         │        │ │ Add to the current task                         │ │  composer, sticky bottom
│         │        │ │ (📎) (◎ Refine)         Claude Sonnet 4.5 ⌄ (■) │ │
│         │        │ └─────────────────────────────────────────────────┘ │            │
│         │        └─────────────────────────────────────────────────────┘            │
└─────────┴──────────────────────────────────────────────────────────────────────────┘
```

- **Header:** `sticky h-13 bg-bg` inside the column, at the top or just under a banner (§4.9), below `--inset` (§6.1). Its first box is `[data-edge]` (the left-edge probe measures it, §18.2 G4). For a chat continued from another one that still exists (`from.kind === 'chat'` with an id, D52) it leads with the parent's title as a link (`text-body text-muted`, at most 40 % of the row, truncate; visually hidden "Continued from " before it) and a faint "/". Title `text-heading`, one line, truncate; click (or Enter on focus) renames inline, Enter saves, Esc cancels. Below 1024 px the header is hidden and the phone top bar holds the title, so renaming happens there (§4.4). Chats from chat apps add a second line in `text-small muted`: "In #eng on Slack", "Telegram". `border-b border-border` appears only once the transcript has scrolled. Right: `⋯` (label "Chat options").
- **Chat menu:** Rename · Pinned ({n}) · Chat details · New related chat · separator · Delete chat… (`danger-text`).
- **Where it came from:** the first row of the transcript, above "Load earlier messages" (`lg:hidden` when the header names the parent, so phones still see it), in `text-small muted` with the name as a `.link`: "Continued from “{title}”" (→ that chat), "Continued from a deleted chat", "From the schedule “{name}”" (→ the schedule), "From your check-in" (the link is on "your check-in", → the check-in form), "From a deleted schedule". A chat that came from nowhere has none.
- **Transcript:** turns 24 px apart; a person's message 16 px above Bothy's reply; activity lines 12 px above the text that follows them.
- **Composer:** `sticky bottom-(--inset)` on a solid `bg-bg` strip (`pt-2`), 16 px below it plus `env(safe-area-inset-bottom)`. No gradient fade.
- **Bothy's picture** (D49): a turn is the blocks between two person messages. Its first reply, stream or steps block gets Bothy's picture (`/api/agent/picture`, `alt=""`, round); with no such block yet, the "Working…" line gets it. At ≥ 640 px every Bothy block sits in the reply column (`pl-10`) and the picture is 28 px at the column's left (`absolute top-0 left-0`); under 640 px it is 20 px on its own line above the turn, so the text keeps the full width. Notices, summaries, outcome lines, queued and paused lines and stray approval cards sit in the reply column with no picture; person messages and day separators keep the full width. In dark mode the picture gets a 1 px `border-strong` ring.
- **Scroll:** the transcript follows new output only while the reader is within 120 px of the bottom; otherwise a small secondary button "Jump to latest" appears centred above the composer.
- **Day separators:** centred `text-caption muted` "Today", "Yesterday", "Saturday 12 October", when the date changes. Per-message times appear in a tooltip on the message actions, not printed.

### 7.2 New chat (empty state)

```
                        (20vh from the top of the column)
▣                                                                  the logo, 40 px, 16 px above the title
What should Bothy work on?                                         text-title (28 px)
Bothy can read your code, draft and research, and it asks          text-reading muted, max 480 px
before doing anything you'd want to check.

┌──────────────────────────────────────────────┐
│ Summarise the open pull requests in acme/web │                   suggestions: rows on one group card
│ What changed in acme/web this week?          │                   (`list-card`), 44 px, text-body,
│ What can you do for our team?                │                   hover bg-subtle, hairlines between
└──────────────────────────────────────────────┘
                                                                   (composer at the bottom as usual)
```

- One "New chat" control only, in the sidebar. `/chat` is the new chat itself, not a "pick a chat" screen.
- Starters (at most four, `GET /api/starters`, SPEC §14.10; the copy comes from the server): two prompts for what is set up (with a repository and GitHub connected, "Summarise the open pull requests in {owner/repo}" and "What changed in {owner/repo} this week?"; with a repository Bothy has only read, "How is {owner/repo} organised?" and "Find where something is handled in {owner/repo}"; with web search, "Research a topic on the web" and "Draft a release checklist for our next version"; otherwise that checklist and "Turn meeting notes into a list of tasks"), at most one link to something not set up yet ("Connect GitHub so Bothy can read your code", "Choose which repositories Bothy can read", "Connect Slack, Discord or Telegram so your team can ask Bothy there", "Add people so they get their own sign-in" for admins; "Have Bothy do something on a schedule" for anyone without schedules of their own), and always "What can you do for our team?". A link row is an accent link; members never get a settings link.
- Clicking a prompt puts its words in the composer (a row may carry longer words than it shows, such as "Research this on the web and give me a short summary with links to sources: ") and focuses it. It never sends.
- The rows share one group card (§11.1) so they read as choices, not as more text under the title. While they load, the space of four rows is kept (no empty card), so nothing moves; if they fail, only "What can you do for our team?" shows.
- The same rows sit under the welcome chat's message until the person writes (§7.3).
- Nothing is stored until the first message is sent (S14). The sidebar row appears then, titled with the first 48 characters until the generated title arrives. Attaching a file first gives the chat its id, since uploads need one, but the screen stays this one (heading, suggestions, no header) until the message is sent.

### 7.3 Messages

| Element | Spec |
|---|---|
| Person's message | `ml-auto max-w-[85%] bg-subtle rounded-lg px-4 py-2.5 text-reading`. Attachments render below as `FileToken`s. Messages added while Bothy was working get a caption under the block: "Added while Bothy was working". A prompt a schedule sent gets "Sent by the schedule", or "Your check-in list" in a check-in's chat, so it doesn't read as something the person typed. On hover or focus a Copy icon button appears beside it (always visible on coarse pointers). |
| Check-in all-clear | A reply that is only the check-in's all-clear token reads "Nothing needs your attention right now." (such chats are listed only once someone writes in them). |
| Welcome message | The welcome chat's one message (a notice from `bothy:welcome`, D43) renders like Bothy's reply, with its picture, in Markdown at reading size, not as a notice line. The starters (§7.2) follow it in the reply column until the person's first message. |
| Bothy's reply | No container, no name; Bothy's picture at the start of each turn (§7.1, D49). The reply column's width, `text-reading`, Markdown per SPEC §9 (security rules unchanged), headings mapped down (§3.2). No caret or dot while it streams (D35). |
| Reply actions | Under the last message of each turn, ghost icon buttons (32 px): Copy, Try again (latest turn only), Details (opens Chat details at that turn). A Refine answer adds a muted "Refined from 3 drafts" before them. Visible on hover and focus-within on fine pointers; always visible under the latest reply and on tap on coarse pointers. |
| Files Bothy made | When a step created or changed a workspace file, the reply ends with `FileToken`s: "launch-checklist.md · Open". |
| Citations | Knowledge citations `owner/repo@sha:path#L10-L42` render as `text-small` links "acme/web · src/auth/redirect.ts, lines 10–42", opening GitHub at that commit; inert in exposed chats (SPEC). |

Markdown styles:

| Element | Style |
|---|---|
| p | `text-reading`, 12 px between paragraphs |
| h1, h2 | `text-heading`, 20 px above, 8 px below |
| h3–h6 | `text-body` 600, 16 px above |
| ul, ol | 20 px indent, 4 px between items |
| inline code | `text-code` at 0.9em, `bg-subtle rounded-sm px-1` |
| code block | `CodeBlock` |
| table | `text-small`, header 500 muted, hairline rows, horizontal scroll inside its own wrapper with a visible scrollbar |
| blockquote | 2 px `border-strong` left rule, `pl-3`, muted |
| hr | hairline, 24 px vertical margin |
| link | `.link` (accent-text, underlined) |
| inert link (exposed chat) | muted host text plus a Copy icon button (behaviour unchanged) |
| image syntax | text link "image: host/…" (unchanged) |

### 7.4 Tool activity

What Bothy does shows as **activity lines**: `text-body muted`, no box, 32 px rows, a 16 px glyph column.

A path inside a repository Bothy got a copy of (`repos/{owner}/{repo}/…`) reads the way citations do (§7.3): "acme/web · src/auth/callback.ts", the repository in plain text and the rest in code. This applies to activity lines, summary lines and decided lines; when a summary already names the repository, its folders are repository-relative ("Got a copy of acme/web and read 3 files in src/auth"). Approval cards and their Details keep the path verbatim (§14.2).

| State | Glyph | Line |
|---|---|---|
| Running | spinner | the "doing" phrase, e.g. "Reading package.json…" |
| Waiting for approval | `clock` in `warning-text` | In `warning-text`. When the pending card renders directly below: "Waiting for your approval" if the viewer can decide it, else "Waiting for approval", since the card names the action. Without a card below (task detail, a step whose request hasn't arrived yet): "Waiting for approval to {ask}" |
| Done | none | the "done" phrase |
| Failed | `alert` in `danger-text` | the "failed" phrase, text stays muted; Details has the reason |
| Denied, expired, blocked | `close` in muted | "Didn't {ask}", shown only when no decided line follows: a denied or expired request shows just its decided line (§8.4), which already says what didn't happen, who decided and why. A blocked step has no decided line, so one `text-small` line under it says why, in the server's plain words ("A deploy to production needs 2 approvers other than you, …", "Bothy can't create the schedule “Daily” during a check-in.") or an admin rule's own reason; a reason stored in the old technical wording reads "Bothy's rules don't allow this."; the full result stays in Details |

Live behaviour:
- Before the first step or the first word, one "Working…" line with a spinner. It disappears as soon as text streams or a step appears. Nothing else indicates streaming; the Stop button and the sidebar spinner show Bothy is busy.
- Only the current step shows live; finished steps of the current group stay listed under the summary while the group is live.

After the group finishes, consecutive finished steps fold into one **summary line** with a chevron (`aria-expanded`):
- If the steps fall into one or two phrase families: join their summaries, e.g. "Read 3 files and searched the web", "Got a copy of acme/web and ran `npm test`".
- Otherwise: "Took {n} steps".
- A single step shows as its own line. Waiting and failed steps and approval cards are never folded, so a summary counts only the steps that folded and never carries the alert glyph; a failed step is said once, on its own line. When only one step would fold, it shows as its own line with no summary.

Each line is a `<button aria-expanded>` that opens **Details** (indented 24 px, `text-small`), in this order here and in Activity's task detail (§10.2), with no nested disclosure:
- "Result": first 20 lines in a `CodeBlock` with "Show all"; file writes show `DiffView`.
- "How it was allowed": "Allowed automatically", "You approved it", "Allowed by a saved permission", "Sam Patel approved it".
- "What Bothy asked for": the action's fields, verbatim (`CodeBlock` of the exact Action JSON). When the Result above shows the action's diff, the JSON reads `"diff": "(shown above)"` instead of printing it twice.
- "Technical": tool name, decision, rule, duration ("Took 1.2 s"), "Contacted api.github.com", and, for outside content, "This came from outside your team ({source}). Bothy treats it with caution.", the source named from the tool: "a web page", "a repository" (GitHub tools and Knowledge search), "a connected tool" (MCP), else "content Bothy read", in the chat's step Details and the task page alike. When "How it was allowed" is shown, the decision word is left out: "Tool fs_write · Rule default:local_write · Took 1 s"

`tool_search` and `use_tool` are never rendered or counted; the tool reached through them is shown as if called directly.

**State and tense.** A line uses the past tense only after the tool's result event has arrived. State comes from the transcript part when the server provides it (S11), otherwise it is derived: approval requested and unresolved → waiting; approved or auto, no result → running; result ok → done; result error → failed; approval denied or expired → denied/expired; decision deny → blocked.

**Phrase table** (`lib/activity.ts`). `{…}` values are inserted verbatim through `Verbatim` (paths, hosts, branches and commands in `text-code`). A step's `{command}` is short: no leading `cd … &&`, only the first line of a multi-line one (a heredoc's body is not its name: "cat > /tmp/t.js <<'E' …"), cut at its first separator outside quotes once over 60 characters; Details keep the whole command. Waiting = "Waiting for approval to {ask}". Denied, expired and blocked = "Didn't {ask}". The *-ing* column feeds decided lines ("You approved {ing}").

| Tool | ask | -ing | doing | done | failed |
|---|---|---|---|---|---|
| `fs_read` | read {path} | reading {path} | Reading {path}… | Read {path} · grouped: "Read {n} files in {dir}" | Couldn't read {path} |
| `fs_list` | look in {dir} | looking in {dir} | Looking in {dir}… | Looked in {dir} | Couldn't open {dir} |
| `fs_write` create | create {path} | creating {path} | Saving {path}… | Created {path} | Couldn't save {path} |
| `fs_write` overwrite | change {path} | changing {path} | Saving {path}… | Changed {path} (+{a} −{r}) | Couldn't save {path} |
| `fs_write` append | add to {path} | adding to {path} | Saving {path}… | Added to {path} | Couldn't save {path} |
| `fs_delete` | delete {path} | deleting {path} | Deleting {path}… | Deleted {path} | Couldn't delete {path} |
| `shell` | run `{command}` | running `{command}` | Running `{command}`… | Ran `{command}` | `{command}` finished with errors |
| `python` | run a Python script | running a Python script | Running a Python script… | Ran a Python script | The script stopped with an error |
| `web_search` | search the web | searching the web | Searching the web for “{q}”… | Searched the web for “{q}” | Web search isn't working right now |
| `web_fetch` GET | read {host} | reading {host} | Opening {host}… | Read {host}{path} | Couldn't open {host} |
| `web_fetch` other | send data to {host} | sending data to {host} | Sending data to {host}… | Sent data to {host} | Couldn't reach {host} |
| `net_allow` | reach {host} | letting Bothy reach {host} | — | Can now reach {host} | — |
| `memory` | remember something | remembering “{first 60 chars}” | — | Remembered “{first 60 chars}” | Couldn't save that |
| `memory` replace | update a saved note | updating a saved note to “{first 60 chars}” | — | Updated a saved note to “{first 60 chars}” | Couldn't update that note |
| `memory` remove | forget something | forgetting “{first 60 chars}” | — | Forgot “{first 60 chars}” | Couldn't remove that note |
| `memory` for everyone (file `TEAM`) | remember something for everyone | remembering “{first 60 chars}” for everyone | — | Remembered “{first 60 chars}” for everyone · grouped: "Remembered {n} things for everyone" | Couldn't save that for everyone |
| `memory` for everyone, replace | update a note for everyone | updating a note for everyone | — | Updated a note for everyone | Couldn't update that note |
| `memory` for everyone, remove | forget something for everyone | forgetting “{first 60 chars}” for everyone | — | Forgot “{first 60 chars}” for everyone | Couldn't remove that note |
| `session_search` | search past chats | searching past chats | Searching past chats for “{q}”… | Searched past chats for “{q}” | — |
| `message` | send a message to {chat} | sending a message to {chat} | Sending… | Sent a message to {chat} | Couldn't send the message |
| `skill_load`, `skill_read` | follow the “{skill}” skill | following “{skill}” | — | Followed the “{skill}” skill | — |
| `schedule_list` | check the schedules | checking the schedules | — | Checked the schedules | — |
| `schedule_create` | create the schedule “{name}” | creating the schedule “{name}” | — | Created the schedule “{name}” | Couldn't create the schedule |
| `schedule_delete` | delete the schedule “{name}” | deleting the schedule “{name}” | — | Deleted the schedule “{name}” | Couldn't delete the schedule |
| `repo_list` | check the connected repositories | checking repositories | — | Checked the connected repositories | — |
| `repo_checkout` | get a copy of {repo} | getting a copy of {repo} | Getting a copy of {repo} ({ref})… | Got a copy of {repo} ({ref}) | Couldn't get {repo} |
| `repo_changes` | review changes in {repo} | reviewing changes | — | Reviewed changes in {repo} ({n} files) | — |
| `repo_open_pr` | open a pull request in {repo} | opening a pull request in {repo} | Opening a pull request… | Opened pull request #{n} in {repo} ↗ | Couldn't open the pull request |
| `github_issues` | read issues in {repo} | reading issues | — | Read issues in {repo} | Couldn't read issues in {repo} |
| `github_pr` | read pull request #{n} | reading #{n} | — | Read pull request #{n} in {repo} | Couldn't read pull request #{n} |
| `github_comment` | comment on #{n} in {repo} | commenting on #{n} in {repo} | — | Commented on #{n} in {repo} ↗ | Couldn't post the comment |
| `github_runs` | check builds in {repo} | checking builds | — | Checked recent builds in {repo} | — |
| `release_deploy` | deploy {repo} to {env} | deploying {repo} to {env} | Deploying {repo} to {env}… | Started deploying {repo} to {env} ↗ (the tool only starts the workflow; the outcome is posted to the chat when the run ends) | Deploy to {env} failed |
| `release_status` | check the {env} deploy | checking {env} | — | Checked the {env} deploy | — |
| `knowledge_search` | search project knowledge | searching project knowledge | — | Searched project knowledge for “{q}” | — |
| `notes` | update the notes for {repo} | updating the notes for {repo} | — | Updated the notes for {repo} | — |
| MCP `mcp__{server}__{tool}` | use {Server}: {Tool title} | using {Server}: {Tool title} | Using {Server}: {Tool title}… | Used {Server}: {Tool title} | {Server}: {Tool title} failed |
| Unknown | use {tool} | using {tool} | Using {tool}… | Used {tool} | {tool} failed |

Tool titles come from the MCP `title` annotation, else the name humanised ("create_issue" → "Create issue").

What only the result knows is read from the words the tool writes: the pull request's number and link from "Opened pull request #482: https://…", the comment's link from "Commented on acme/web#482: https://…", the run's link from a started deploy's "Run 1003: https://…", and repo_changes' file count from "3 changed files:". Only `repo_open_pr`, `github_comment` and `release_deploy` get a ↗ link, and only to an `https://` address; any other result holds what a page or a command printed and never becomes a link.

### 7.5 Composer, Refine and model picker

```
┌──────────────────────────────────────────────────────────────────┐
│ [report-q3.pdf · 2.1 MB  ×]                                       │  FileTokens, only when present
│ Message Bothy                                                     │  textarea text-reading, 1 row, grows to 40vh
│                                                                   │
│ (📎) (◎ Refine)                           Claude Sonnet 4.5 ⌄ [↑] │  toolbar, 32 px controls
└──────────────────────────────────────────────────────────────────┘
Refine is on: Bothy writes 3 drafts, checks them and sends the best. About $0.20 for this reply.
```

| Part | Spec |
|---|---|
| Box | `border border-border-strong rounded-lg bg-surface p-3 shadow-card`; focus is the `focus` border plus the 4 px halo (`shadow-focus`, §3.5) |
| Placeholder | "Message Bothy". While Bothy works: "Add to the current task". Paused: "Bothy is paused". No helper line. |
| Keys | Fine pointer: Enter sends, Shift+Enter adds a line. Coarse pointer: Enter adds a line, send with the button. "/" at the start opens commands. Esc stops a running task. ↑ in an empty composer edits the last message where supported. |
| Commands | Menu above the composer (`rounded-lg`, `shadow-overlay`): "/new New chat", "/stop Stop", "/pin Keep a file in mind", "/model Change model", "/compact Summarise this chat so far" |
| Attach | Ghost icon button with the `paperclip` icon, label "Attach files"; file picker. Drag and drop onto the chat column shows an overlay inside the column: 1 px dashed `control`, `bg-subtle`, "Drop files to attach". |
| File tokens | Name, size, remove button "Remove {name}". Uploading: spinner in the token, Send waits. Failed or too large: the token shows the alert glyph and the name in `danger-text`, with no label of its own, and the reason ("{name} is larger than {limit}.") is the one line under the box. |
| Refine | `ToggleButton` (`aria-pressed`) with the `target` icon, label "Refine". Off: ghost `text-muted`. On: `bg-accent-subtle text-accent-text` (the icon follows the colour, never filled). Tooltip (fine pointer): "Bothy writes several drafts, checks them and sends the best one. Slower, and costs more." The state belongs to the chat, starts from Settings → Models → "Refine by default", and is read when each message is sent. |
| Refine cost line | Under the box, `text-small muted`, only while Refine is on: "Refine is on: Bothy writes {N} drafts, checks them and sends the best. About {estimate} for this reply." (S10). Without an estimate: "… Costs about {m}× a normal reply." Without either: "… Slower, and costs several times more." |
| Model picker | Ghost small button with the model's friendly name (`text-small`; `muted` for the default, `text` when overridden) and a chevron. Menu opens upward: "Main model · Claude Sonnet 4.5" (checked by default), "Quick tasks model · Claude Haiku 4.5", separator, the other connected models grouped by provider (group labels `text-caption muted`). The choice applies to this chat. Members see the same names as admins: `GET /api/models` marks the first model of each slot (`slot`), so nobody needs the settings to read them. Hidden when only one model is connected. Under 400 px the label shortens to the short name ("Sonnet 4.5"). |
| Send | Icon button 32 px, primary (`bg-accent text-on-accent shadow-primary`), `arrow-up`, label "Send". Empty composer: `bg-subtle text-faint`, `aria-disabled`. |
| Stop | While a task runs, Send becomes a small **secondary** icon button with the `stop` glyph, named and tooltipped "Stop", never a filled button (the one fill is Send). If text is typed during a run, both show: Stop, and Send (adds to the task). |
| Paused | Textarea disabled, placeholder "Bothy is paused", Send and Attach disabled. No visible line under the box: the paused banner (§4.9, with Resume for admins) and the placeholder already say it, so "Bothy is paused." is screen-reader only, for `aria-describedby` on the disabled controls. The disabled Attach button is `quiet` (`text-faint`, no fill), so it doesn't look pressed. |

### 7.6 Notices and errors in the chat

A notice is a system line at the column edge: `text-small muted`, no box, at most one action as a link button, optional Details. Errors use `danger-text` and the alert glyph. Every error is said once.

| Event | Copy | Action |
|---|---|---|
| Provider error | "{Provider} had a problem answering." | "Try again"; Details: raw message |
| Every model failed | "No AI model could answer right now." | "Try again" |
| All failed on auth (member) | "Bothy can't reach its AI model right now. An admin has been told." | — |
| All failed on auth (admin) | "{Provider} stopped accepting Bothy's key." | "Update key" |
| Backup model answered | nothing (the attempt is in task details) | — |
| Stopped by the person | "You stopped Bothy." | — |
| Step limit | "Bothy paused after {n} steps to check in." | "Keep going" |
| Loop detected | "Bothy stopped because it kept repeating the same step. Try asking in a different way." | — |
| Task spending limit | "Bothy stopped because this task reached its {$0.50} limit." | Admins: "Keep going" (raises once) |
| Daily limit | "Bothy has reached today's {$5.00} spending limit. It can start again at midnight ({workspace time zone})." | Admins: "Change limit" |
| Compaction | "Older messages were summarised so this chat can keep going." | — |
| Auto-pin | "Pinned {file} so Bothy keeps it in mind." | — |
| Becomes exposed (once per chat) | "From now on, Bothy will ask before sending anything out of this chat." | "Why?" → Details: "This chat has used private information and content from outside your team, like a web page, a repository or a connected tool's reply. To keep that information from leaking, Bothy now asks first. A new related chat starts clean." (searching the team's repository or a reply from its MCP server counts too: anyone who can open an issue or a pull request, or whatever the server reads, may have written them) |
| Needs code, code is off | "Bothy can't run code on this server, so it couldn't run `{command}`." The failed step's own activity line is hidden, since this notice names the command and links the fix. | Admins: "How to turn it on" → Security |
| Paused | "Bothy is paused. Your message will be answered when it's resumed." | — |
| Message queued | "Bothy will read this when it finishes the current step." | — |
| Upload failed | "Couldn't attach {file}: {it's larger than 20 MB / this file type isn't supported}." | — |

### 7.7 Chat menu items

- **Chat details** (`Drawer`, 400 px, full screen on phones):
  ```
  Chat details                                         ×
  Started by Maya Chen · Today at 10:02 · Web
  Model          Claude Sonnet 4.5
  Refine         Off
  Cost so far    $0.42
  Length         Getting long. Bothy will summarise older messages soon.   ← only above 70 %
  › Technical details
      Context: 23,104 of 200,000 tokens (12%)
      Breakdown table (Part · Tokens), plain rows, no colours: Built-in rules, Personality,
      Memory, Skills, Pinned, Tools, Summary, History, Tool results, Turn context, Reply reserve
      Cache hits 64% · Time to first word 0.8 s
      Per-step calls: model id, attempts, time to first word, cost
      Model id anthropic/claude-sonnet-4-5 [Copy]
      Chat id ses_01m… [Copy]
  ```
  The Model row and the model picker never show a model id: a model without a name from the server reads as its friendly name ("Claude Sonnet 4.5"), and a chat's model that is no longer connected reads "A model that's no longer connected".
- **Pinned** (`Drawer`): title "Pinned to this chat", sentence "Bothy keeps these in mind for every reply in this chat.", list of pins (name, "Unpin" ghost), secondary "Pin a file".
- **New related chat**: no dialog, since nothing is lost: a clean chat of this one opens at once, listed under it and selected in the sidebar (D42). It starts with the notice every clean chat has; its first message names it. A failure says "Couldn't start a new chat. Try again." in a toast.
- **Delete chat** (`Dialog` 440, one dialog for the chat menu and the sidebar's ⋯): title "Delete “{chat title}”?" in curly quotes, so it names the chat when opened from a sidebar row while another page shows; body "The messages will be deleted. Anything Bothy already did stays done, and its record stays in Activity. This can't be undone.", whose first sentence becomes "The messages will be deleted; the chats continued from it stay in your list." when listed chats came from it; buttons [Delete chat] (danger) [Cancel] (focused). Then toast "Chat deleted." Those chats stay where they are; a schedule's next newest result heads its series.
- **Rename**: inline in the header.

---

## 8. Approvals and releases

The card is still rendered from the structured Action by the policy module, never from model text. What changes is the copy and the layout. The server's `Card` gains plain fields (S2) so Slack, Telegram and Discord show the same words; until then the client builds them from the tool name and Action with the same tables (`lib/cardcopy.ts`).

### 8.1 Card anatomy

```
┌──────────────────────────────────────────────────────────────────┐
│ ▤  {Question}                                                     │ type icon 18 px muted (≥ 640 px) · text-heading
│    {Consequence and requester}                                    │ text-body muted, optional
│                                                                   │
│    ! {Warning}                                                    │ 0–2 lines, alert glyph + text-small warning-text
│                                                                   │
│    {Label}    {Value}                                             │ plain grid, no tint: labels text-small muted (96 px), values text-body
│    Changes    8 lines removed · Show                              │ "Show" expands DiffView inside the card
│                                                                   │
│    [Primary verb] [Allow for this chat] [Deny]                    │ primary (danger when destructive) · secondary · secondary
│    Always allow sending to #eng in Slack…                         │ link button, only when offered
│    From “Clean up staging notes” · Expires in 15 min · Details    │ text-small muted
└──────────────────────────────────────────────────────────────────┘
```

| Part | Spec |
|---|---|
| Container | `.card`: `bg-surface border border-border rounded-lg p-5 shadow-card` (`p-4` under 640 px). At ≥ 640 px a two-column grid (`grid-cols-[1.25rem_minmax(0,1fr)] gap-x-3`): the type icon (§3.7) in the first column, everything else in the second; under 640 px the icon is hidden and the content takes the full width. No coloured risk pill, no header strip, no footer band. |
| Status | **One signal** (D50). The border stays neutral whatever the state, the card carries no status word ("Waiting for you"), and nothing on it is tinted except code and diffs. In chat the signal is the waiting line above the card (§7.4); in the inbox, the row's one status (§8.8). |
| Question | `card.question` (S2). Identifiers inside it render through `Verbatim` in `text-code` (bidi and zero-width characters visualised, punycode hosts). |
| Consequence | `card.consequence`, only when it matters: "This can't be undone." for irreversible and financial actions; for deploys "This updates {env} for everyone using it." When someone else or somewhere else asked: "Sam Patel asked in #eng on Slack." |
| Warnings | `card.warnings`, plain copy (§8.3), at most two lines; more go under Details. |
| Fields | Key/value grid `grid-cols-[96px_minmax(0,1fr)] gap-x-4 gap-y-2`. Values through `Verbatim`; mono only for paths, commands, hosts and branches; long values wrap with `overflow-wrap: anywhere`, never ellipsis. Commands, message text, recipients, websites, repositories, branches, environments and versions are always shown in full. "Show" folds only diffs and file contents. |
| Buttons | From `card.choices` (§8.3). `mt-4 flex flex-wrap gap-2`. Under 480 px they stack full width, primary first. |
| Meta | `mt-3 text-small muted`: "From “{chat title}”" (links to the chat) or "Requested by {name} in “{chat}”"; for a request from a chat app the server's context line instead, never a link, and left out when the asked line already says who asked and where (§8.4); then the expiry; then a "Details" disclosure trigger. |
| Expiry | "Expires in 15 min" (whole minutes), "Expires in 2 h", "Expires in less than a minute"; switches to `warning-text` under 2 minutes. Never a bare countdown. A schedule's request (one that parks when its wait ends) shows no expiry while it waits, since it doesn't expire then. A parked request has no expiry in the meta line: "Bothy stopped waiting. Approve within {23 h} to let it carry on." (from `park_until`; without it, "Approve to let it carry on.") is a `text-body` line under the consequence and asked lines, the most important fact on the card, and the meta line reads "From “Morning briefing” · Details". |
| Details | "Bothy's explanation (written by the AI, not checked)", collapsed, only when present · "Why Bothy asked: {plain reason}" · "If nobody answers: it will be denied" (or "it will wait") · "Who can approve: Maya Chen, or any admin" · "Exact action" (`CodeBlock` of the Action JSON) · tool, rule and approval id, each with Copy. |
| Deny | Pressing Deny shows an inline "Reason (optional)" field with [Deny] and [Cancel]; Enter submits. The reason is stored as `Resolution.Comment` and shown to the requester. Bothy is told who denied and their reason (SPEC §5.7 g), so its reply can answer it, also in a chat app. |
| Submitting | The pressed button shows a spinner and its "-ing" label; the others are disabled. |
| Submit failed | Notice in the card: "Couldn't send your decision. Try again." with the buttons re-enabled. When the server refuses with a reason (409, 403), trying again can't help: the card reloads the request and says why instead, "This request was already decided." (an inline card folds into its decided line and announces it) or the server's reason as a sentence ("You already voted.", "You asked for this, so someone else has to approve it."). |

### 8.2 Copy per action

The table is the copy contract for S2 and `lib/cardcopy.ts`.

| Action | Question | Primary button | Consequence | Fields |
|---|---|---|---|---|
| `fs_write` create | "Create {path}?" | "Create file" | — | Content ("{n} lines · Show"). No File row: the question shows the path verbatim |
| `fs_write` overwrite | "Change {path}?" | "Save changes" | — | Changes ("+{a} −{r} · Show") |
| `fs_write` append | "Add to {path}?" | "Add to file" | — | Adds ("{n} lines · Show") |
| `fs_delete` | "Delete {path}?" | "Delete file" (danger) | "This can't be undone." | Content ("{n} lines · Show") |
| `shell` | "Run this command?" | "Run command" (danger when it deletes files) | "This can't be undone." when it deletes files (`rm`, `find -delete`, …) | Command (full) · In (working folder) |
| `python` | "Run this Python script?" | "Run script" | — | Script ("{n} lines · Show") |
| `web_fetch` non-GET, first contact | "Send data to {host}?" | "Send data" | — | Website · Data ("Show") |
| `message`, first contact | "Send a message to {chat}?"; when Bothy knows the chat's name (a group chat it was added to, or a person in a chat app): "Send a message to “Ops” on Telegram?" | "Send message" | — | To ("Telegram chat 5512", or with the name "“Ops” on Telegram (chat 5512)": the id stays beside the name, since anyone in a chat can change its title) · Message (full). The first-contact warning names the chat the same way. |
| `memory`, from outside content | "Remember this?" | "Remember" | — | Note (full) |
| `memory` for everyone (an admin approves) | add: "Remember this for everyone?" · change: "Change something Bothy remembers for everyone?" · remove: "Forget this for everyone?" | "Remember for everyone" · "Save change" · "Forget for everyone" | "Bothy will use this in every chat, for everyone." (remove: "Bothy will stop using this in every chat.") | Note (full) · Was (change only). No In row: the question says it is for everyone |
| `notes`, from outside content | "Update the notes for {repo}?" | "Update notes" | — | Change ("{n} lines changed · Show", a prose diff) |
| `schedule_create` | "Create the schedule “{name}”?" | "Create schedule" | — | When · Sends to (where results go, as the server's `deliverText` says: "Bothy on the web" for an account, "Maya Chen's messages on Slack" for someone in a chat app, "#eng on Slack" for a channel; a set chat, always the owner's own, reads the same and never shows its id) · Does (first 3 lines · Show) |
| `schedule_delete` | "Delete the schedule “{name}”?" | "Delete schedule" (danger) | "This can't be undone." | When |
| `net_allow` | "Let Bothy reach {host}?" | "Allow for this chat" (the gateway offers no one-time choice here) | — | Website |
| `repo_open_pr` | "Open a pull request in {repo}?" | "Open pull request" | — | Branch ("{head} → {base}") · Title · Changes ("{n} files, +{a} −{r} · Show") |
| `github_comment` | "Comment on #{n} in {repo}?" | "Post comment" | — | On ("{PR or issue title}", left out when the title is unknown rather than repeating "#{n} in {repo}") · Comment (full) |
| `release_deploy` | "Deploy {repo} to {env}?" | "Approve deploy" | "This updates {env} for everyone using it." | Environment · Version ("{ref} ({short sha}) · {commit subject}") · Workflow (the file that runs, e.g. `deploy.yml`) · Inputs (when set, one `key=value` per line) · Changes ("{n} commits since the last deploy · View on GitHub ↗") · Approvals (§8.5). The repository is named in the question. |
| MCP tool | "Use {Server}: {Tool title}?" | "Allow" (danger when the class is irreversible) | "This can't be undone." when irreversible | `field_map` fields, labels humanised |
| Admin class | "Change Bothy's settings?" | "Change settings" | — | What (setting labels) · Value |
| Financial | "Pay {amount} to {recipient}?" | "Pay {amount}" (danger) | "Money will leave your account. This can't be undone." | To · Amount |
| `skill_install` | "Install the “{skill}” skill?" | "Install skill" | — | Source (the address; "From" is the meta line's chat) |
| Unknown tool | "Let Bothy use “{tool}”?", the name humanised, quoted and in lower case ("Let Bothy use “report export”?") | "Allow" | — | the Action's fields, labels humanised |

Danger fill rule (server-provided `card.tone`, S2): `destructive` when the side-effect class is irreversible or financial (a shell command that deletes files is irreversible, like `fs_delete`), except `release_deploy`, whose safeguard is its approvers.

The GitHub cards (`repo_open_pr`, `github_comment`, `repo_checkout`) have no Websites field: they name the repository, and the host they reach is Bothy's own GitHub setting, not something the AI chose. Other actions that name the websites they reach list them under Websites.

### 8.3 Choices and warnings

| `card.choices` | Control | Notes |
|---|---|---|
| `allow_once` | Primary button with the verb from §8.2 | Shortcut A |
| `allow_session` | Secondary "Allow for this chat"; on a request from someone's own chat in a chat app, "Allow in {Name}'s chat" (the name cut to 24 characters), since the approver reads it elsewhere; on a scheduled run's request, "Allow for this schedule", since the grant covers the schedule's next runs, each in a new chat | Shortcut S. Shown only when `card.choices` includes it; the gateway never offers it for deploys, admin-class actions or exposed chats, and the UI never adds it. When `allow_once` is absent (as for `net_allow`), this is the primary button. |
| `allow_always` | Link button under the row, only when offered (never for irreversible actions, never for a channel's requests): "Always allow {narrow description}…", and for a command "Always allow this command…" (reprinting the command outweighed the buttons; the dialog names it in full); on someone's request from their own chat in a chat app, "Always allow for {Name}…" | Opens a dialog (440): title "Always allow this?"; body "Bothy won't ask again before {narrow description, e.g. “sending messages to #eng in Slack”}, in any chat. You can remove this in Settings → Security." (for someone in a chat app: "… before {narrow description} for {Name} on {App}. …"); buttons [Always allow] [Cancel]. No shortcut. |
| `deny` | Secondary "Deny" | Shortcut D. Never red. |

Warnings (from `Decision.Warnings`, rewritten server-side, S2):

| Warning | Copy |
|---|---|
| First contact, recipient | "Bothy hasn't sent anything to {recipient} before." |
| First contact, domain | "Bothy hasn't sent data to {host} before." |
| First pull request in a repository | "This is the first pull request Bothy opens in {repo} for you." ("… for this group chat." in a group chat). Earlier pull requests opened for other people don't count. |
| Exposed chat | "This chat has seen private information and content from outside. Check carefully before letting anything leave." Not on actions where nothing leaves, such as deleting a file or throwing away edits. |
| Untrusted origin | "This request came from content Bothy read (a web page, repository or message), not from a person." |
| Memory for everyone, from a chat that read outside content (not Exposed) | "This chat has seen content from outside your team. Check that a person asked for this." An Exposed chat gets the Exposed chat line instead, since the note reaches every chat. |

### 8.4 Decided state

The card stays in the transcript (fixes audit `27`). It collapses to one line, `text-small muted`, with a "Show" link button that re-opens the read-only card (no buttons; the decision at the bottom only where no decided line is visible above the card, so a card opened with "Show" leaves it out). The activity line above changes to the matching tense. "{time} · Show" stays together on one line (`whitespace-nowrap`, with the "·" before it), so "Show" is never alone on a line and no line ends in "·"; the same holds in the Done tab.

Directly after the step line it decided, an approval drops the object, in the style of "Alex approved in Slack": "You approved it · 11:35 PM · Show" ("Alex Kim and Maya Chen approved it"). The full "You approved {ing}" stays wherever the line stands alone: denied, expired and cancelled lines (whose step line is hidden), task detail and the Approvals Done tab. Grant lines keep their words ("You allowed changes to files in archive/ for this chat").

| Outcome | Line (glyph) |
|---|---|
| Approved once | "You approved {ing} · 10:42" (check, `accent-text`), e.g. "You approved deleting notes/staging.md · 10:42" |
| Allowed for this chat | "You allowed {plain grant, e.g. “changes to files in notes/”} for this chat · 10:42" (check) |
| Always allowed | "You always allow {plain grant} · 10:42" (check) |
| Denied | "You denied {ing} · 10:42", plus “{reason}” when given (close, `danger-text`) |
| Decided by someone else or elsewhere | "Sam Patel approved in Slack · 10:42" / "Sam Patel denied in Slack · 10:42"; a chat approver is named with their app instead: "Maya Chen · Slack approved · 10:42" (in the Done tab "Maya Chen · Slack approved deleting notes/a.md") |
| Multi-approver approved | "Alex Kim and Maya Chen approved {ing} · 11:02" |
| Multi-approver denied | "Alex Kim denied {ing} · “Not before the freeze ends” · 10:40" |
| Expired | "Expired: Bothy didn't {ask}" (close, muted). On a task's "What Bothy did": "Nobody answered, so Bothy didn't {ask}", or for a multi-approver request with some approvals "Only 1 of 2 approvals came in, so Bothy didn't {ask}" (its Details: "Not enough approvals came in time"); a request ended by Stop while it waited: "Cancelled: Bothy didn't {ask}", and its step no longer reads "Waiting for approval" |
| Cancelled | "Cancelled: Bothy didn't {ask}" when the chat was stopped (the "You stopped Bothy." notice gives the reason), e.g. "Cancelled: Bothy didn't send a message to #ops · 9:47 PM · Show" / "Cancelled by {requester}" for a withdrawn request ("Cancelled by you" for the viewer). A multi-approver request its requester withdraws is stored as denied by their vote, and so is a request someone in a chat app withdraws with "Cancel request"; every surface (chat, inbox, task detail, Releases, chat-app cards) still says "Cancelled by {requester}", never "denied". |

Chat apps (SPEC §5.9) edit a card to its decided line in place of the question, with ✓ or ✗ for the glyph and "You" for the approver who reads it: "✓ You approved deleting notes/staging.md.", "✗ Sam Patel denied deleting notes/staging.md in Slack." A pending chat card starts each warning with ⚠ for the alert glyph.

**Requests from chat apps.** A teammate's or a channel's request (`approver.team`) goes to admins here and to the people an admin marked "Can approve" (§11.5), in their own direct messages. Admins decide it like any card; members never see it. The card:

- **Asked line** (under the question): "Maya Chen (@maya) asked on Slack." for someone's own chat; "Maya Chen asked in #eng on Slack." for a channel; when a schedule started it (`requester.scheduled`) nobody asked: "From #eng on Slack." for a channel's, "From Maya Chen's schedule on Slack." for a teammate's. Whose request it is comes from `requester.kind` (`teammate` | `channel`; a channel's requester always has `chat`), never from which fields happen to be set.
- **Meta line:** the server's context instead of "From …", never a link, since admins can't open these chats: "Maya Chen's direct messages on Slack" or "#eng on Slack". When the asked line (or "From #eng on Slack.") is shown, it already says who asked and where, so the meta line leaves the context out: "Expires in 58 min · Details".
- **Buttons:** as §8.3, with the person's name on the standing choices. A channel's request offers only the verb and Deny.
- **Details:** "Who can approve: an admin, or someone who can approve in a chat app". Someone who can't decide reads "Waiting for an approver."
- **Deny reason:** the field's help text (muted, under the field, `aria-describedby`; not a placeholder, which typing removes) says who reads it: "Maya Chen will see this." or "People in #eng won't see this, but Bothy will." (a channel's outcome line leaves the reason out; a channel's schedule reads the same).
- **Inbox row:** "Maya Chen · Slack" as the source.
- **Decided line:** names the chat approver with their app (above); "Cancelled by Maya Chen" when the person who asked withdrew it. A standing choice on a teammate's request says whose it is, as its button did: "You allowed changes to files in notes/ in Maya Chen's chat", "You always allow changes to files in notes/ for Maya Chen" (a channel's request has no standing choices).

### 8.5 Multiple approvers ("1 of 2")

```
┌──────────────────────────────────────────────────────────────────┐
│ Deploy acme/web to production?                                    │
│ This updates production for everyone using it.                    │
│ Sam Patel asked in #eng on Slack.                                 │
│                                                                   │
│ Environment   production                                          │
│ Version       main (4f2c1a9) · Fix login redirect loop            │
│ Changes       12 commits since the last deploy · View on GitHub ↗ │
│ Approvals     1 of 2                                              │
│               (AK) Alex Kim approved · 10:32                      │ 24 px avatar rows
│               One more from you, Priya Rao or any admin           │ text-small muted; only for the viewer who decides or asked
│                                                                   │
│ [ Approve deploy ]  [ Deny ]                                      │
│ Expires in 2 h · Details                                          │
└──────────────────────────────────────────────────────────────────┘
```

| Viewer | Decision area |
|---|---|
| Eligible, not voted | [Approve deploy] [Deny] |
| Eligible, already approved | Text "You approved. Waiting for Priya Rao, Maya Chen or any admin." (no buttons); the Approvals row leaves out its "more from" line, so this is said once |
| The requester | Text "You asked for this, so you can't approve it yourself." (the Approvals row above keeps its "{n} more from …" line, which this sentence relies on). Plus secondary "Cancel request" (the requester's vote never counts) |
| Not eligible | Text "Waiting for Priya Rao." (who can still approve); the Approvals row leaves out its "more from" line |

- The viewer is "you" wherever they would be named (§14.2): "One more from you, Priya Rao or any admin", "You approved · 12:21 AM", "Who can approve: you". The server words who can still approve for the viewer, leaving out the requester and anyone who already voted.

- One deny resolves it as denied; the k-th distinct approval resolves it as approved. Every vote is shown with name and time. Chat-app cards route to every eligible approver (SPEC); deciding anywhere updates every surface live through `approval.resolved` and vote events (S4).
- Until S4 lands, deploy cards render as single-approver cards without the Approvals row.

### 8.6 Pull request card

```
┌──────────────────────────────────────────────────────────────────┐
│ Open a pull request in acme/web?                                  │
│                                                                   │
│ ! This is the first pull request Bothy opens in acme/web for you. │
│                                                                   │
│ Branch     bothy/fix-login-redirect → main                        │ text-code
│ Title      Fix login redirect losing the return address           │
│ Changes    3 files, +24 −6 · Show                                 │
│            src/auth/redirect.ts · +3 −2                           │ one line per file, always listed
│            src/auth/redirect.test.ts · +4 −0                      │
│            CHANGELOG.md · +2 −1                                   │
│                                                                   │
│ [ Open pull request ]  [ Deny ]                                   │
│ From “Login bug” · Expires in 15 min · Details                    │
└──────────────────────────────────────────────────────────────────┘
```

The diffs start closed everywhere, the inbox too, so the buttons stay in reach; the file list keeps what changes in view. "Show" opens each file's diff in `DiffView`. After approval the activity line reads "Opened pull request #482 in acme/web ↗".

### 8.7 Deploy follow-up

After approval, the originating chat (and the web chat) gets activity lines updated in place. On the web every deploy line on screen shares one read of `GET /api/releases`, so a chat with several deploys asks once; it reads again on `release.dispatched` and `release.finished` (once for the copies an admin who asked gets) and on a resync, never on a timer: those events reach everyone who can see the line, the requester and admins and the people the approval names (everyone when it names members), and the list carries the deploys of approvals the viewer may see:
1. "◌ Deploying acme/web to production… View on GitHub ↗"
2. "Deployed acme/web to production in 3 min" (check glyph), or
3. "! Deploy to production failed at “Run migrations” · View logs on GitHub ↗" (`danger-text`), or
4. "! Bothy stopped tracking the deploy of acme/web to production · Check on GitHub ↗" (alert glyph and text muted, never red) when the deploy's status is `unknown`: Bothy lost the run or stopped following it, so the outcome is only on GitHub.

Succeeded, failed and `unknown` are final: no event follows them.

### 8.8 Approvals inbox (`/approvals`)

```
Approvals
(Waiting 3 | Done)                                                   tabs on a track; count only on Waiting

For you 4                                                            text-heading label, then the count, regular weight, muted
┌────────────────────────────────────────────────────────────────┐
│ ⇡ Deploy acme/web to production?                                │   first item expanded = full card
│   …full card as in 8.5…                                         │
└────────────────────────────────────────────────────────────────┘
┌────────────────────────────────────────────────────────────────┐
│ ▤ Delete notes/staging.md?                                    › │   collapsed rows are cards too
│   Clean up staging notes · 2 min ago                            │   line 2: source · age
└────────────────────────────────────────────────────────────────┘
┌────────────────────────────────────────────────────────────────┐
│ ▭ Run python3 build_agenda.py …            Expires in 10 min  › │   the one status: an urgent expiry
│   Edinburgh team offsite · 30 min ago                           │
└────────────────────────────────────────────────────────────────┘

Waiting for others 1                                                 only when non-empty
┌────────────────────────────────────────────────────────────────┐
│ ⇡ Deploy acme/api to production       ◷ 1 of 2 approvals      › │   who or what it waits for
│   API release · 20 min ago                                      │
└────────────────────────────────────────────────────────────────┘
```

- One column, no duplicate card. `/approvals/:id` opens the same list with that item expanded and scrolled into view (fixes `30`).
- Admins, while `team_access` lasts (§11.5): a neutral `Notice` above the Waiting list, "People in chat apps no longer link to Bothy accounts. Check who can use Bothy and who can approve in People." with "Open People", since their requests now arrive here.
- Sections: `section-title` with the count after it as a plain number (`text-heading font-normal text-muted`, outside the heading, so its name stays "For you"), then a list of cards 8 px apart.
- Collapsed row: a card-like button (`min-h-14 rounded-lg border border-border bg-surface px-4 py-3 shadow-card`, hover `bg-subtle`, `aria-expanded="false"`) with the type icon (18 px, muted), the title, line 2 and a trailing `chevron-right`. The title is the card's row title, `text-body` 500, which names what the card is about where the question alone can't tell two rows apart: a command "Run {command}?" (verbatim; the row's ellipsis cuts it and the card shows it in full), a note "Remember “{first 60 characters}”?" ("Remember “…” for everyone?", "Forget “…” for everyone?", "Change “…” for everyone?" for what's remembered for everyone), a message "Send a message to {recipient}?" (worded as on the card's To line). Other rows show the question. Line 2 at every width (`text-small muted`, truncated): source and age ("Clean up staging notes · 2 min ago", "Alex Kim · Slack · 6 min ago"). The right slot holds **at most one status**: for you, "Expires in 3 min" in `warning-text` when under 15 minutes, or "Waiting since 10:42" (muted) when parked, else nothing; waiting for others, a `clock` and who it waits for (`waitingFor`, muted): "Waiting for {name}", "Waiting for an admin" (admin-only and chat-app requests), "Waiting for an approver" (a name Bothy can't show), "{k} of {n} approvals" (several approvers). Under 640 px the status moves under line 2. Clicking or Enter expands the row in place and collapses the previous one.
- After a decision the row collapses (160 ms), the next "For you" item expands and takes focus, and the live region says "Approved. Next: {question}" or "Nothing else is waiting."
- **Item types** (all in the same card shell, S12):

| Type | Question | Expanded content |
|---|---|---|
| Action approval | from §8.2 | the card |
| MCP tool review (admins) | "Review changes to {Server} actions"; row meta "1 action changed" / "2 actions changed". When every action is new (a server just added): "Review {Server}'s new actions", "1 new action", and [Approve actions] | One row per action: plain name, what it does, "{Server} says this only reads information." when its definition declares `readOnlyHint` (shown only: the select keeps its class, since a server's own word never lowers it), "What this does" select (Only reads information / Changes things inside Bothy / Sends data out / Can't be undone; a changed action starts at the class chosen before), "Show what changed" (changed actions only): a prose `DiffView` of the old and new description when that is the only change, else the `DiffView` of the definition. Buttons: [Approve changes] [Turn off {Server}]. Only what the server offers now is listed; an action that was turned off stays off when the server changes it and never comes up here. |
| Changed skill (admins) | "Review changes to the “{skill}” skill" | "Show what changed" (diff of SKILL.md and files; `.md` files as prose). Buttons: [Approve] [Turn off skill]. |
| Someone waiting to be let in (admins; where the chat app can't say who is in the team: Telegram, and Discord when it can't check) | "Someone new messaged Bothy on {App}" | "{display name} ({handle}) wrote: “{first 120 chars, Verbatim}”"; without a username, "{display name} wrote: …" ("Someone" without a name). Details: the account's id on {App}, with Copy. Buttons: [Let in] (A) [Block] (D). Live region: "Let in. Next: …". Slack and Discord members get in on their own; guests, people from other companies and bots never reach this list. |
| New group chat (admins) | "Bothy was added to {chat} in {App}" | Lines, each only when known: "Added by {name}."; "{n} people are in this chat." (Discord: "{server} has {n} members."). Then one line for the case: Discord "Turning this on lets all {n} members of {server} message Bothy, here and in direct messages." (without a count "… lets everyone in {server} …"; without the server's name, "The Discord server" / "the Discord server", never "this server", which is Bothy's host); Slack, when people from other companies are in it, "People from other companies are in this channel. They'll see Bothy's replies, though Bothy won't answer them."; Telegram "Anyone in this group can use Bothy once it's on, not only people you let in.". Then "What Bothy can do there" (`OptionList`): "Answer questions" — "Bothy reads and answers. It can't change anything." (the default, whoever is in the channel), "Do work, with approval" — "Bothy can change files, run code and send things. An approver checks each step first.". Then "Bothy answers only when mentioned." when it does. Buttons: [Turn on] (A) [Ignore] (D). Turn on saves the choice with it. |
| Note suggestion | "Update the notes for {repo}?" | the card with a prose diff |

- **Done tab:** decided lines (§8.4) grouped Today, Yesterday, Earlier (last 30 days), each group's lines in one group card (`list-card`) with hairlines between them, newest decision first (by when each was decided, not asked). A cancellation names what it cancelled, as every line here does: "Max Ortiz cancelled deploying acme/web to production" ("You cancelled …"), where the transcript, with its card above, keeps "Cancelled by Max Ortiz". Here the decided sentence is the page's main content, `text-body` in ink, with one muted meta line under it: "12:13 AM · From “Clean up staging notes” · Show" (a chat app's request shows its context line in place of the link; another person's request, such as a deploy an approver voted on, has no link, since their chat wouldn't open). The link keeps its underline. A chat that can't be opened (404) reads "This chat doesn't exist or was deleted." with no Try again.
- **`/approvals/:id` no longer waiting:** "This request was already decided." followed by its decided line, also when someone else decides it while it is open there (instead of the card vanishing); a request that can't be looked up: "This request doesn't exist, or you can't see it." Neither has a button: this is the Approvals page.
- **Keyboard:** J/K or ↑/↓ move between rows, Enter expands and moves focus into the card, A / S / D decide the expanded card. Inside a choice (the group chat's "What Bothy can do there") keys move between the options and decide nothing; Tab reaches the buttons. A held key decides one item; repeats are ignored until release (current behaviour). Shortcuts appear in button tooltips on fine pointers ("Delete file (A)") and in the shortcuts dialog. Never as chips.
- **Empty:** Waiting: Bothy's logo (32 px) beside "Nothing needs your approval. When Bothy needs someone's OK, it shows up here."; when requests wait for others below, only "Nothing needs your approval." Done: "No decisions in the last 30 days." (no picture).

### 8.9 Releases

Deploy environments are configured in Settings → Connections → GitHub (§12.2). History and status live in Activity → Releases (§10.4). Deploys start from a chat ("Deploy main to staging"). Schedules can never deploy; "deploy" never appears in a schedule's permissions list.

---

## 9. Knowledge

`/knowledge` has three tabs: **Projects · Memory · Skills**. Projects and Skills are row lists in one group card (`list-card`, §11.1). Documents (each Memory section, a project's notes, a skill's instructions) sit on a `Card` of their own, so every tab reads as the Settings groups do; a row that opens something hovers edge to edge in its card and ends in a muted `chevron-right` unless a control of its own sits there (§3.7). The "Search code and notes" field sits on the Projects tab only, under the tabs and above the list, and only once at least one repository has been read: it searches neither Memory nor Skills, and ⌘K Search covers everything else.

### 9.1 Projects (`/knowledge`)

```
Knowledge                                                    [ Add repositories ]   admins, secondary
[Projects]  [Memory]  [Skills]
[ ⌕ Search code and notes                                                      ]   once a repository was read

┌────────────────────────────────────────────────────────────────────────────────┐
│ acme/web                                                          Up to date  › │   one group card
│ Customer web app built with Next.js. Notes updated 2 days ago.                  │
├────────────────────────────────────────────────────────────────────────────────┤
│ acme/api                                                       ◌ Reading… 40% › │
│ Go API behind the web app.                                                      │
├────────────────────────────────────────────────────────────────────────────────┤
│ acme/infra                                                    Couldn't read   › │
│ Too large to read (more than 50,000 files).                                     │
└────────────────────────────────────────────────────────────────────────────────┘
```

- Rows are 56 px and link to the project page: the link stretches over the row, which hovers `bg-subtle` edge to edge, with Read now, Read again and Details above it and a muted › at the end. Line 1: `owner/repo`, `text-body` 500, sans (a name here). Line 2 (`text-small muted`): the first sentence of the notes, then "Notes updated {relative time}." Before notes exist: "Bothy is writing notes for this project." Under 640 px line 2 takes up to two lines (`line-clamp-2`): with notes, the summary is cut to the first and "Notes updated …" takes the second, so the date is never cut off however long the summary.
- Status at the right (`text-small`): "Up to date" (muted); "◌ Reading… {n}%" (muted); "Out of date" (muted) + "Read again" link (admins); "Couldn't read" (`danger-text`) + "Details" (muted, like every Details, since it opens the raw message) with a plain reason ("GitHub says Bothy no longer has access to this repository."). A repository too big to read ("Too large to read (more than 50,000 files).") fails the same way every time, so it gets neither Details nor "Read again"; "Read again" is offered only for temporary reasons (GitHub limiting requests, or out of reach).
- "Add repositories" opens Settings → Connections → GitHub at the repository picker.
- Empty, no GitHub (admin): "Connect GitHub to give Bothy your code. It reads your repositories, keeps notes on each project and can answer questions about them." + primary "Connect GitHub". Member: "Ask an admin to connect GitHub so Bothy can read your code." (no button).
- Empty, GitHub connected, nothing chosen: "Choose which repositories Bothy can read." + "Choose repositories".

### 9.2 Project page (`/knowledge/projects/:owner/:repo`)

```
← Knowledge
acme/web                                                        [ Edit ]  ⋯

┌──────────────────────────────────────────────────────────────────┐
│ Bothy suggested 2 changes to these notes.                Review  │  Notice (bg-subtle), only when present
└──────────────────────────────────────────────────────────────────┘

┌──────────────────────────────────────────────────────────────────┐
│ What this is                                                     │  rendered NOTES.md on a Card (20 px padding),
│ Customer-facing web app, deployed from main.                     │  text-reading, same Markdown styles as chat
│                                                                  │
│ How to build and test                                            │
│ Run `pnpm install`, then `pnpm test`. …                          │
│                                                                  │
│ Conventions …   Important decisions …                            │
└──────────────────────────────────────────────────────────────────┘
Written by Bothy on Oct 13 · Edited by Priya Rao on Oct 15 · History          12 px under the card, text-small muted, one date form
```

- `⋯` menu: "Ask Bothy about this project" (new chat with the repository in context), "Read the repository again" (admins), "View on GitHub ↗", "Technical details" (dialog: commit read `4f2c1a9`, the one the last finished read used (`indexed_sha`), files indexed 1,204, last read today 09:12, notes size 3.1 KB of 8 KB), "Stop reading this repository…" (admins; confirm "Stop reading {name}?", body "Bothy won't use {name} to answer questions, and its project notes and their history will be deleted. This can't be undone.", [Stop reading] (danger) [Cancel]; removes it from the allowlist; the project notes and their history are deleted with the index, SPEC §8.3).
- **Edit in place:** the rendered document becomes a sans `TextArea` in the card's place (never a box on a card) (`text-reading`, auto-grow, Markdown source) in the same position; header actions become [Save] (primary) [Cancel]. Under 640 px [Save] [Cancel] sit under the text box instead of in the header (moved, not duplicated), since focusing the box scrolls the header away. ⌘S / Ctrl S saves. Esc cancels, after "Discard your changes?" if anything changed: body "This can't be undone.", buttons [Keep editing] and [Discard changes] (danger). Saved: toast "Saved."
- **Conflict** (`If-Match` failed): warning `Notice` above the editor, only "Someone else changed these notes while you were editing." While it is unresolved the primary becomes [Compare versions] (saving again would meet the same conflict), opening a 720 dialog with the prose diff, a secondary [Use their version] under it, and the footer [Cancel] [Keep mine]: on phones Cancel takes the left slot and only closes. The memory sections work the same way.
- **Size:** above 80 % of the cap a `text-small warning-text` line under the header: "These notes are almost full. Shorten them so Bothy has room to add more." At the cap Save shows "These notes are too long to save. Remove something first."
- **History** (720 dialog): one entry per change, read as the Approvals Done tab reads a decision: the change as a sentence in ink ("**Bothy** added 3 lines", "**Maya Chen** edited", the name 500), then a muted meta line, "2 days ago · From “Login bug” · Show"; Show opens its prose `DiffView` under it.
- **Prose diffs** (History, Compare versions, Suggested changes) diff the raw text but show each line cleaned: a leading `#`, `-` or `*` goes (headings in medium weight, list items with "•"; a numbered item keeps its "1." since the order is part of it), code ticks go as in search snippets, link addresses stay. Only the changed lines and one line around them show, with "…" between groups, however long the document.
- **Suggested changes:** "Review" opens a 720 dialog listing the pending `notes` approvals as standard approval cards (question "Update the notes for acme/web?", the Change field already open so the prose diff shows straight away, "{n} lines changed · Hide", warning for outside content, [Update notes] [Deny]). The same items appear in the Approvals inbox. When the last one is decided the dialog closes and the notice disappears.
- **First read in progress:** in place of the notes: "Bothy is reading acme/web and writing notes. This usually takes a few minutes." with a muted line "Read 340 of about 1,200 files".
- **Too big to read:** the notice "This repository is too big for Bothy to read (more than 50,000 files)." with no action, and in place of the notes "You can write notes for this project yourself." + [Write notes].
- **Couldn't read** for another reason: a danger notice with the plain reason as its own sentence ("GitHub says Bothy no longer has access to this repository.", "Bothy couldn't reach GitHub. It will try again later.", or "Bothy couldn't read this repository." when there is no plainer one), never both. Admins get the next step in it: "Read again" for temporary reasons, "Check the GitHub connection" (to §12.2) when access was lost. In place of the notes "You can write notes for this project yourself." + [Write notes], as for a repository too big to read. In the Projects list the reason without a plainer one reads "An unexpected error stopped the read." beside Details.

### 9.3 Search (`/knowledge/search?q=`)

```
[ ⌕ login redirect                                                         × ]
[All]  [Code]  [Notes]                                In: [ All repositories ⌄ ]

acme/web · src/auth/redirect.ts, lines 10–42                    Open on GitHub ↗
┌──────────────────────────────────────────────────────────────────────────┐
│ 12  const next = url.searchParams.get('next') ?? '/';                    │  CodeBlock, max 6 lines,
│ 13  return safeRedirect(next);                                           │  matches in <mark>
└──────────────────────────────────────────────────────────────────────────┘
acme/web · Project notes · How to build and test
Run pnpm install, then pnpm test. End-to-end tests need a running API…       plain snippet

[ Ask Bothy about “login redirect” ]                                         secondary; new chat with the query
```

- Filters are a `Segmented` control and a repository select. Results grouped per file, at most 20, then "Show more". Each file (or notes section) is a card of its own (`rounded-lg border bg-surface px-4 py-3 shadow-card`, 8 px apart, like the Approvals rows), its excerpts tinted inside it. Line numbers in `faint`. The path line reads "lines a–b", never `#L10-L42`; the full citation `owner/repo@sha:path#L10-L42` is in each result's Details.
- No results: "Nothing in your projects or notes matches “{q}”."

### 9.4 Memory (`/knowledge/memory`)

```
Knowledge
[Projects]  [Memory]  [Skills]

Bothy reads this at the start of every chat. Change anything that's wrong or out of date.   text-reading muted

Remembered for everyone                                                [ Edit ]   admins only
Bothy uses this in every chat, for everyone, including chat apps.     text-small muted
┌────────────────────────────────────────────────────────────────────────────┐
│ Our deploy day is Thursday.                                                │   each document on a Card
└────────────────────────────────────────────────────────────────────────────┘

About you                                                              [ Edit ]
┌────────────────────────────────────────────────────────────────────────────┐
│ Maya is the CTO at Acme. Prefers short answers with links to the code.     │
└────────────────────────────────────────────────────────────────────────────┘

Things Bothy remembers                                                 [ Write ]
┌────────────────────────────────────────────────────────────────────────────┐
│ Bothy adds things here as you work together.                               │   empty: text-body muted
└────────────────────────────────────────────────────────────────────────────┘

History ›                                                              only once there is history
```

- "Remembered for everyone" (TEAM.md, one for the whole team, `GET`/`PUT /api/memory/team`) comes first, then the two personal sections (USER.md, MEMORY.md), all rendered Markdown in `text-reading`.
  - Admins: the note "Bothy uses this in every chat, for everyone, including chat apps." under the heading; Edit/Write like the other sections; empty: "Add what Bothy should know in every chat, like your deploy day."
  - Members: read-only, no Edit or Write and no "almost full" line (they can't remove anything); the note "Bothy uses this in every chat, for everyone. Only admins can change it."; empty: "Nothing yet. Ask Bothy to remember something for everyone, and an admin can approve it."
  - Bothy never writes it on its own: someone asks Bothy to remember something for the team, and an admin approves the card "Remember this for everyone?" (§8.2). Signed-out visitors see nothing (sign-in first, as for every page).
  - Live updates: admins follow `memory.updated` with file `TEAM` (an admin broadcast); members see changes when they open the page.
- Each section's document sits on a `Card` 8 px under its heading (and note), its empty line inside it, so the page reads as the Settings groups do.
- "Edit" turns a section the reader may change into a sans `TextArea` in its card's place, with [Save] [Cancel] (under the box on phones, §9.2). No monospace, no byte meter.
- Near the cap (> 80 %): `text-small warning-text` under the section: "This is almost full. Remove anything Bothy no longer needs." At the cap Save shows "This is too long to save. Remove something first."
- Saved: "Saved" next to the button for 2 s, then "Changes apply from your next message." (for everyone's section: "Changes apply from everyone's next message.") stays in `text-small muted` until the page is left.
- History (720 dialog): the same entries as the notes' History (§9.2), with actor names ("You", "Bothy", "Maya Chen (admin)"): "**Bothy** added 1 line", then "Things Bothy remembers · 3 h ago · In “Fix login redirect” · Show". The meta line leads with the section's name, so changes to what's remembered for everyone read "Remembered for everyone · …" (`/api/memory/team/history`), with a chat link only for a chat the reader can open.
- Empty section: one muted line saying what it is for, About you "Tell Bothy about your role and how you like answers.", Things Bothy remembers "Bothy adds things here as you work together.", with "Write" in the heading spot where Edit sits. "History ›" is hidden while the history is empty.
- Personal memory is per account here. People in chat apps have notes of their own that Bothy keeps for them, private to them like their chats; channels have none of their own (SPEC). What's remembered for everyone reaches all of them, and someone in a channel with standard tools can ask for it too (an admin approves).

### 9.5 Skills (`/knowledge/skills`)

- A skill is named for people everywhere (list, page heading and `<title>`, switch label, inbox, activity lines): the SKILL.md front matter's title or first heading, else the folder id in words, sentence case ("Release notes", "Incident review"). The id stays in Files paths and Details.
- Rows: name (`text-body` 500), one-line description (`text-small muted`), right: a `Switch` (admins) or the word "On"/"Off" and a muted › (members). A changed skill shows admins only a small "Review" button instead of the switch, with no "Needs review" word; members see "Off" in muted text, like any skill that's off. The row's link stretches over it and the switch sits above it.
- Admin page actions: [Add skill] (upload a zip; only once there is a skill, since the empty state offers it) and `⋯` → "Check for changes".
- Skill page (`/knowledge/skills/:name`): back link, title, rendered SKILL.md (read-only `Markdown`) on a `Card`, then a "Files" disclosure whose files are rows of one group card, each opening a `CodeBlock` inside its row (one that didn't load offers "Try again", and is asked for again when reopened). A changed skill shows "This skill changed, so it's off until an admin approves the changes below." with "Approve" (toast "Approved."), and its Files disclosure starts open.
- Empty: "No skills yet. Skills teach Bothy how your team does specific things, like writing release notes." + "Add skill" (admins).

---

## 10. Activity

```
Activity
[Tasks]  [Schedules]  [Releases]  [Spending]  [Audit log]
```

Tabs are route links on a track (§6.2). Every row list here (tasks per day, schedules and their examples, schedules in chat apps, a schedule's recent runs, environments and release history, the spending breakdown, the audit log per day) sits in one group card (`list-card`, §11.1) under a `section-title` label (a day's name, "Examples", "Environments", "History", "By person"), as the Approvals Done tab and the Settings groups are. A row that opens something hovers `bg-subtle` edge to edge in its card (the card's `overflow-hidden` rounds the first and last) and ends in a muted 16 px `chevron-right`, unless a control of its own (a switch, ⋯) sits there; a row that expands in place turns its chevron down. The forms (a schedule, the check-in) sit on group cards too, with `SettingsRow`s for the short choices. "Releases" appears once an environment exists; "Audit log" for admins only. Under 640 px the tabs scroll sideways with no wrap and no visible scrollbar; the current tab is scrolled into view on every route change, and while more tabs sit past the right edge a 24 px fade there says so. The page heading is always "Activity", for members too; each tab keeps its own actions (New schedule, Set up environments ›, the range picker, Check the log) and names itself in the browser tab's title ("Spending · Activity").

### 10.1 Tasks (`/activity`)

```
[ Everyone ⌄ ]  [ Anywhere ⌄ ]  [ Any result ⌄ ]                          admins see "Everyone"

Today                                                                     section-title
┌────────────────────────────────────────────────────────────────────────────────┐
│ Fix login redirect                                           $0.12    10:42  › │
│ Maya Chen · Web · 6 steps                                                      │
├────────────────────────────────────────────────────────────────────────────────┤
│ Daily standup summary                                        $0.03    09:00  › │
│ Schedule · Posted to #standup on Slack                                         │
├────────────────────────────────────────────────────────────────────────────────┤
│ Deploy acme/web to production                                  —      08:55  › │
│ Sam Patel · Slack · Waiting for approval (1 of 2)                              │
└────────────────────────────────────────────────────────────────────────────────┘

Yesterday                                                                 40 px above each later day
```

- 56 px rows linking to task detail, a muted › at the end. Line 1: title (chat title or schedule name) `text-body` 500; cost and time right-aligned, `text-small muted tabular-nums`. Line 2 `text-small muted`: who (person's name, or "Schedule") · where (Web, Slack, Telegram, Discord, API) · outcome.
- Outcome words: nothing extra when done ("{n} steps"); "Stopped" (muted); "Waiting for approval" / "Waiting for approval (1 of 2)" (`warning-text`); "Failed: {plain reason}" (`danger-text`); "Hit the spending limit" (`warning-text`); "Interrupted", "Skipped" (muted). A refined answer adds "Refined".
- Filters (`Select`s): "Everyone / {person}" (admins), "Anywhere / Chats / Schedules / Chat apps / API", "Any result / Needs attention / Failed".
- Grouped by day, "Show earlier" at the bottom.
- Members: the heading reads "Activity" as for admins, no person filter.
- Under 640 px: time moves into line 2 ("Maya Chen · Web · 10:42"); cost stays at the right of line 1.
- Empty: "Nothing yet. Tasks Bothy works on, in chats or on a schedule, show up here." + "Start a chat".

### 10.2 Task detail (`/activity/tasks/:id`)

```
← Activity
Fix login redirect                                                   [ Open chat ]
Maya Chen asked in Web · Today at 10:42 · Took 2 min 10 s · Cost $0.12      text-small muted

What Bothy did                                                            text-heading
  Got a copy of acme/web (main)
  Read 6 files in acme/web
  Ran `npm test`
  Changed src/auth/redirect.ts (+12 −3)
  Maya Chen approved opening a pull request in acme/web
  Opened pull request #142 in acme/web ↗

Answer
  The return address is dropped in the login form… (3 lines)       Show all

› Technical details
```

- Steps are `ActivityLine`s (§7.4) with approvals included; each expands its Details.
- "Technical details" (closed, `text-small`) holds the old trace as plain tables. A refined answer's Refine block comes first, since every model call of a refinement reads "agent" and its drafts would otherwise sit under a dozen tables: "3 drafts, 2 rounds. Draft 2 was sent; 2 of 3 drafts reached its answer.", then each draft, critique and correction in its own nested disclosure, rendered as Markdown with links off rather than as code (a disclosure inside a disclosure is allowed, a card inside a card is not). Then Model calls (Model id · Started · Time to first word · Duration · Tokens in / cached / out · Cost · Fallback attempts); Tool calls (Tool · Exact action · Decision · Rule · Approval id · Sites contacted · Duration); Task id [Copy].
- Refined answers add "Refined from 3 drafts · 2 of 3 agreed" to the meta line.
- A task still going ends the meta line with "Working…" ("Waiting to start" while queued), since it may have no step to show yet; one waiting for approval says so on its waiting step instead.
- Next step: when the viewer can decide a waiting step, its line ends with a "Review it" link to `/approvals/{id}`. A task that failed because GitHub stopped accepting Bothy's access gets "Check the GitHub connection ›" on the failed step (admins only, to Connections → GitHub), since "Open chat" can't fix it.
- Details follow the chat's order (§7.4): Result, How it was allowed, What Bothy asked for, Technical.
- Not found: title "Can't open this task", "This task was deleted, or it belongs to someone else." + "Go to Activity". The server answers another person's task as it does a deleted one (404, so nobody learns which tasks exist), so the copy names both.

### 10.3 Schedules (`/activity/schedules`)

```
Schedules                                                        [ New schedule ]

┌────────────────────────────────────────────────────────────────────────────────┐
│ Check-in                                                          (off)    ⋯   │   one group card
│ Every 30 min, 8:00–22:00 · Looks through your list and tells you only when …   │
├────────────────────────────────────────────────────────────────────────────────┤
│ Daily standup summary                                             (on)     ⋯   │
│ Weekdays at 9:00 · Posts to your chats · Next: tomorrow at 9:00                │
├────────────────────────────────────────────────────────────────────────────────┤
│ Weekly PR digest                                                  (on)     ⋯   │
│ Mondays at 8:00 · Posts to your chats · Last run failed: the GitHub token …    │
└────────────────────────────────────────────────────────────────────────────────┘

Examples                                     with fewer than 3 schedules of their own
┌────────────────────────────────────────────────────────────────────────────────┐
│ Monday pull request summary                                                  › │
│ Mondays at 9:00 · Uses GitHub                                                  │
├────────────────────────────────────────────────────────────────────────────────┤
│ Morning news on a topic                                                      › │
│ Weekdays at 8:00 · Uses web search                                             │
└────────────────────────────────────────────────────────────────────────────────┘
```

- Row: the whole row opens the schedule (its name's link stretches over it; the switch and `⋯` sit above it). Line 1 the name (`text-body` 500) across the full width minus the switch and `⋯` (72 px); line 2 `text-small muted`, wrapping freely: when · where · next run or last problem (problems in `danger-text`). Times read in the schedule's own zone, so one in another zone than the viewer's names it after the repeat text: "Every day at 6:00 AM, Tokyo time". The name never shares a column with the meta, so it never wraps one word per line at 360 px (fixes `44`).
- Where: "Posts to your chats". Accounts have no chat app (SPEC §5.16), so each result of their schedules gets its own chat in their list, grouped under the schedule (§4.2).
- `⋯`: Run now · Edit · History · Delete… (`danger-text`, confirm dialog "Delete the schedule “{name}”?", body "It stops running. Its past results stay in your chats. This can't be undone."). Run now is audited with who pressed it ("Started the schedule “{name}” by hand"); its toast "Schedule started." carries "Open chat", which opens the chat the run writes in (`RunScheduleResp.session_id`), and no action when the server names none.
- "New schedule" (primary) is always in the header.
- A result posted to a chat opens with an italic line "From the schedule “{name}” · change it under Schedules" (a check-in: "From your check-in · change it under Schedules"), since nobody asked for it there.
- **Check-in** is always listed first (the heartbeat). While its list is empty (`checkin.empty` from `GET /api/schedules/examples`), turning its switch on saves nothing and opens `/activity/schedules/checkin?on=1` instead, since a check-in with nothing to check would do nothing; Save there turns it on. Edit opens `/activity/schedules/checkin`: one group card: "How often" (Every 15 min / 30 min / hour) and "Between" (active hours, two time selects) as `SettingsRow`s, then "What to check" (sans `TextArea` editing the HEARTBEAT.md checklist as prose) stacked in the card's last row, help "Bothy looks through this list regularly and messages you only when something needs attention.", placeholder "One thing per line, for example: Tell me if a deploy to production failed overnight". When every non-empty line of the file is a list item, the box shows plain lines (the leading `- [ ] `, `- [x] ` or `- ` stripped) and saves each line back as `- [ ] {line}`, since the scheduler reads the text as it is; a file with any other line is shown verbatim so nothing is rewritten. Cost line under the card, `text-small muted`: "About $0.05 a day with {Quick tasks model}." [Save] [Cancel]. While the box is empty and the server suggests lines (`checkin.lines`: a failed deploy to one of the repository's environments, pull requests waiting more than 2 days for review, a failed build on the default branch), a `LinkButton` "Use suggested lines" sits under the help; it fills the box, one line each, and moves focus to it. Nothing is saved until Save. Save with an empty box while the check-in is on, or being turned on, saves nothing and shows "Add something to check first." under the box, since the scheduler skips an empty list.
- **Examples** (`GET /api/schedules/examples`, D45): a section titled "Examples" under the list, only when the server offers any (at most three that fit what is connected and whose name the person doesn't already use, none once they have three schedules of their own; SPEC §14.10). Each row is one link (`hover:bg-subtle`, like a settings row that opens a page): the name (500), `text-small muted` "{repeat text} · Uses GitHub" or "· Uses web search", and a muted › at the right. It opens `/activity/schedules/new?example={id}`. Nothing is created until the form is saved. For admins, examples that can't run yet follow (`ready: false`), so a new install still shows what schedules do: their second line reads "{repeat text} · Connect GitHub first" or "· Turn on web search first", and the row opens `/settings/connections/github` or `/settings/connections`; the form ignores such an id.
- Empty (only Check-in exists and no examples): under it, "Add your own, like a Monday summary of open pull requests." The header's "New schedule" is the action. A failed examples call counts as none.
- **In chat apps** (admins, only when there are any, or when they failed to load): a section under the list for the schedules people and channels set up in chat apps (`GET /api/team/schedules`). Help `text-small muted`: "Schedules people set up in chat apps. Their results go to that chat." Rows: the name (500); `text-small muted` "{owner} · {App} · {repeat text} · Next: {time}" (the repeat text names the schedule's zone when it isn't the viewer's, as in the rows above: "Weekdays at 9:00 AM, Tokyo time"), "… · Off" when it is off, or "… · Not scheduled" when it is on with no next run; a `Switch` named after the schedule (`aria-label` "{name}", like the rows above) that only turns it on or off; the same `⋯` as the rows above (`aria-label` "More actions for {name}"), so the switches line up with the list above, holding one danger item "Delete…" → confirm: title "Delete “{name}”?", body "It stops running. Past results stay in that chat. This can't be undone.", buttons "Delete schedule" (danger) and "Cancel". Editing them happens in their chat. If the list doesn't load, the section shows "These schedules didn't load. Check your connection and try again." with "Try again" instead of going missing.

**New or edit schedule** (`/activity/schedules/new`, `/activity/schedules/:id`), one column on two group cards, as a settings page:

```
← Schedules
New schedule

┌────────────────────────────────────────────────────────────────────────────────┐
│ What should Bothy do?                                                          │   stacked rows, 16 px padding
│ [ Summarise yesterday's merged pull requests in acme/web.                    ] │
├────────────────────────────────────────────────────────────────────────────────┤
│ When                                                                           │
│ [ Every weekday ⌄ ]  at  [ 09:00 ⌄ ]                                           │
│ Runs weekdays at 9:00 AM London time. Each result gets its own chat in your    │
│ list. Next: Mon Oct 19, Tue Oct 20, Wed Oct 21.                                │
├────────────────────────────────────────────────────────────────────────────────┤
│ Name                         [ Summarise yesterday's merged pull requests ]    │   SettingsRow; filled from the first words
└────────────────────────────────────────────────────────────────────────────────┘

› More options
┌────────────────────────────────────────────────────────────────────────────────┐
│ Model                                          [ Quick tasks model ⌄ ]         │   SettingsRows (§11.1), stacked on phones
│ Stop a run above                               [ $ 0.50 ]                      │
│ Only run between                               [ 08:00 ⌄ ] and [ 20:00 ⌄ ]     │
│ If Bothy was off                               [ Run when it's back ⌄ ]        │   / Skip that run
│ Time zone                                      [ London (Europe) ⌄ ]           │   by city; the IANA id is the stored value
│ Allowed without asking                                                         │   stacked
│   [ ] Read websites on api.example.com                                         │
└────────────────────────────────────────────────────────────────────────────────┘

[ Create schedule ]  [ Cancel ]
```

- "When" options: Every day, Every weekday, Every week on {day}, Every month on day {n}, Once on {date}, Custom. "Custom" reveals "Repeat rule" (a cron expression in a text field, `text-code`, help "Five fields: minute, hour, day, month, weekday. Example: 0 9 * * 1-5 means weekdays at 9:00."). Cron appears nowhere else. The preview line comes from `/api/schedules/preview`.
- Accounts have no chat app, so results always come to their chat list: the preview says "… Each result gets its own chat in your list." ("The result gets …" for Once on) instead of a "Send results to" field with nothing to choose. A new schedule saves it as the web.
- **From an example** (`?example={id}`): the form fetches the examples and prefills What should Bothy do?, When, Name (kept when the task is edited), Model and "Run when it's back", in the person's own time zone; an id the server no longer offers opens the blank form. A prompt that still holds one of its example's placeholders ("[your topic]") can't be saved: the field error reads "Replace [your topic] first." and the placeholder is selected, so typing replaces it. Brackets someone typed themselves never block.
- Time zones read by city: in the preview "London time", "New York time" ("UTC" as it is), and in the Time zone combobox "London (Europe)".
- Time zone starts as the person's own (Settings → Account); the preview and the saved schedule use the zone chosen here.
- "Allowed without asking" lists the pre-authorised narrow grants as plain sentences (`lib/grants.ts` descriptions), unticked until chosen. Deploys never appear.
- Errors: empty prompt "Tell Bothy what to do."; invalid repeat rule "That repeat rule isn't valid. Example: 0 9 * * 1-5 means weekdays at 9:00."; a save the server refuses shows its reason, which retrying can't change: "Mon 19 Oct 2026 09:00 has already passed. Choose a later time.", "It would never run between 08:00 and 20:00, the hours it may run in. Choose a time inside them." A Run now that can't start says why under the row ("The check-in list is empty, so there's nothing to check yet. Add something to it first.", "Bothy is paused by an admin, so nothing new starts.").
- Edit pages add "Recent runs" below (rows linking to task detail; the title is when it ran, "Yesterday at 10:58 PM", the meta line how it went, "Posted to your chats" or "Failed: {plain reason}" (a finished check-in counts its steps instead, since an all-clear posts nothing), the cost at the right; the page already names the schedule, so the rows don't) and the page `⋯`: "Run now" (as in the list's `⋯`, with its toast and "Open chat"; a refusal shows its reason under the form), a separator, "Delete schedule…".
- Buttons: "Create schedule" / "Save schedule" (primary), "Cancel".

### 10.4 Releases (`/activity/releases`)

```
Releases

Environments                                                              section-title
┌────────────────────────────────────────────────────────────────────────────────┐
│ production · acme/web                                                 Deployed │
│ main · 4f2c1a9 · Fix login redirect loop · 2 days ago · approved by Sam Patel  │
├────────────────────────────────────────────────────────────────────────────────┤
│ staging · acme/web                                                ◌ Deploying… │
│ main · 9e1b3c0 · Add audit export · started 2 min ago · View on GitHub ↗       │
└────────────────────────────────────────────────────────────────────────────────┘
To deploy, ask Bothy in a chat, for example “Deploy acme/web to staging”.       text-small muted

History                                                                   section-title
┌────────────────────────────────────────────────────────────────────────────────┐
│ Deploy acme/web to production · main at 4f2c1a9          Deployed  2 days ago › │   your own: opens the task
│ Sam Patel asked · Sam Patel and Alex Kim approved · took 3 min                 │
├────────────────────────────────────────────────────────────────────────────────┤
│ Deploy acme/web to production · main at 77de012            Denied  3 days ago   │   another person's: no ›, room kept
│ Jordan Lee asked · Alex Kim denied: “Not before the freeze ends”               │
└────────────────────────────────────────────────────────────────────────────────┘
```

- Environment rows: name · repository, status word at the right ("Deployed" muted, "◌ Deploying…" muted, "Waiting for approval (1 of 2) ›" `warning-text`, a link to `/approvals/{id}`, "Failed" `danger-text`, "Last request denied" muted, never a bare "Denied", which beside the last deploy would read as the environment's own state, "Not tracked" muted), line 2 the last-deploy facts. When the status is a waiting or denied request, line 2 starts with "Last deployed": "Last deployed Oct 15 · main · 4f2c1a9 · “Fix login redirect loop” · approved by Alex and Maya Chen", so it never reads as if that request was approved. A request its requester cancelled leaves the row on the last deploy.
- The environment row describes the environment whoever deployed (members included): its newest deploy, and with two or more running "◌ 2 deploys running". The newest running deploy shows in its environment row only and joins History when it ends; the others running beside it are History rows ("Deploying…").
- "Not tracked" is a deploy whose status is `unknown` (Bothy lost the run or stopped following it): never "Failed", no "took", and "Check on GitHub ↗" when the run is known. The page stops refreshing once no deploy is running.
- History rows link to task detail, only for the viewer's own deploys and requests (another person's task can't be opened, even by an admin): such a row is a link stretched over it, its GitHub link above it, with a muted › at the end; the others keep the ›'s room so the times line up. A request its requester withdrew reads "Cancelled" (muted) with "{requester} asked · Cancelled by {requester}"; one stopped while it waited "Cancelled", "{requester} asked · stopped before it was decided"; one nobody decided in time "Expired", "{requester} asked · only 1 of 2 approvals came in time" or "… · nobody answered in time". Under 640 px the status word leads the meta line ("Deploying… · You asked · Alex approved · 4 min ago") and the title wraps, since rows differ at its end. External links keep their label and ↗ together.
- Everyone can read this page. Admins see "Set up environments ›" in the page actions (to §12.2).
- The tab is hidden until an environment exists. A server without releases answers 404, which reads as no environments; any other failure shows "This page didn't load." with "Try again" (§13.2), never "No deploy environments yet". With environments but no deploys yet: "No deploys yet. Ask Bothy in a chat, for example “Deploy main to staging”."

### 10.5 Spending (`/activity/spending`)

```
Spending                                          [ Today | 7 days | 30 days ]   Segmented in the header

┌────────────────────────────────────────────────────────────────────────────────┐
│ $42.10 in the last 30 days                     figure text-figure, the rest muted │   one group card, as General's
│ Today: $1.20 of the $3.00 daily limit                              text-body muted │   Spending group
│ ▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░   ProgressBar 4 px │
├────────────────────────────────────────────────────────────────────────────────┤
│ (bar chart, 160 px tall, one bar per day, neutral bars, two gridlines labelled  │
│  $2 and $4, three date labels: first, middle, last)                            │
└────────────────────────────────────────────────────────────────────────────────┘

By person                                                           section-title
┌────────────────────────────────────────────────────────────────────────────────┐
│ Maya Chen                                                           $18.20  › │   opens Tasks for them
│ Alex Kim · Slack                                                     $3.40    │   someone in a chat app
│ #eng · Slack                                                         $2.10    │   a channel
└────────────────────────────────────────────────────────────────────────────────┘
Show all

By model
Claude Sonnet 4.5 · Anthropic                              $35.00
Qwen3 14B · Ollama                                           Free

By schedule
Daily standup summary                                       $0.90

Top chats
Fix login redirect                                          $0.42

› Technical details                                                 tokens in / out, cached share, calls per model
```

- Headline: one sentence, never KPI cards: the amount in `text-figure`, the period after it in muted `text-body`, as General's spending summary reads. It, today's line, the meter (the card's full width) and the chat apps' line share one group card with the chart under a hairline. The meter fills with `accent`; at 80 % it turns `warning-text` with the line "Bothy will stop starting new work at $3.00."; at 100 % `danger` with "Bothy reached today's limit. New work waits until midnight." + "Change limit" (admins).
- Chat apps' share (admins, when chat apps have one: `chat_limit_micros` from `/api/usage/today`): a line under the meter, `muted` "Chat apps today: $0.65 of their $1.80 share"; at 80 % `warning-text` "Chat apps have used 85% of their $1.80 share of today's limit."; at 100 % `danger-text` "Chat apps used their $1.80 share of today's limit. Their new work waits until midnight." + "Change share" (→ People, where the share is set). This line replaces the `chat_budget_80` banner (§4.9).
- Chart: `chart` bars, hovered or focused bar `text`, gridlines `border`, axis labels `text-caption muted`. With 7 bars or fewer, the amount is printed above each bar. Every bar is focusable with a tooltip "Tue 14 Oct: $1.20". `role="img"` with a summary label ("Spending by day, 15 September to 14 October, highest $4.10 on 9 October") plus a visually hidden table. Days follow the workspace time zone.
- Lists: 40 px rows, name left, money right `tabular-nums`, top 5 then "Show all" (expands in place). Names only (S8). In "By person", people in chat apps and channels pay from lines of their own: their name, then " · {App}" in `muted`; they link nowhere (their tasks and chats aren't on the web). "By model" names the provider the same way, " · Anthropic" in `muted`. Rows link where possible: a person to Tasks filtered by them, a chat to the chat; those end in a muted › and the others keep its room, so the amounts line up. Local models show "Free". Unknown prices: "Price not set" (muted) with "Set price" for admins (Advanced → Model prices).
- Members: their own total, their limit if any, no By person; the heading still reads "Activity".
- Under 640 px: 7 bars with day initials; the 30-day view labels only the 1st and 15th. Nothing is clipped (fixes `46`).
- Empty: "Nothing spent in this period."

### 10.6 Audit log (`/activity/audit`, admins)

```
Audit log                                              [ Check the log ]   ⋯ (Export)
[ Actions | Website access ]                                               Segmented
[ Anyone ⌄ ]  [ Any action ⌄ ]  [ Last 7 days ⌄ ]

Today                                                                     section-title
┌────────────────────────────────────────────────────────────────────────────────┐
│ 10:42   Maya Chen    Approved deleting notes/staging.md                      › │   one group card per day
│ 10:41   Bothy        Asked to delete notes/staging.md                        › │
│ 10:12   Sam Patel    Signed in                                               › │
│ 08:57   Bothy        Blocked from sending to paste.example.net               › │
└────────────────────────────────────────────────────────────────────────────────┘
```

- Rows 40 px: time (`tabular-nums muted`, 56 px), actor name (120 px), one-line summary (S16). A result word appears only when not a success and the summary doesn't already say it: "Blocked" (left out after "Blocked from sending …"), "Failed" in `danger-text`. Reaching the spending limit gets no word: it needs attention but isn't a failure, as in Tasks. No "ok" pills (fixes `47`).
- Memory rows say what changed, never the note itself: "Remembered something", "Updated a saved note", "Forgot something" (the same for Bothy's memory steps); for what's remembered for everyone "Remembered something for everyone", "Updated a note for everyone", "Forgot something for everyone", and an admin's edit "Edited what Bothy remembers for everyone".
- Clicking a row expands it in place (its › turns down): Who, When, Where (chat link), Task (link) as the approval card's plain key/value grid (labels `text-small muted`, values `text-body`), and a "Raw record" disclosure with the redacted JSON in a `CodeBlock`. Website access rows open nothing, so they have no ›.
- "Check the log" runs the hash-chain check and shows a `StatusLine` under the header: "The log is intact. 1,284 entries checked." or (danger) "The entry from 12 Oct at 10:42 doesn't match the ones before it. Entries from there on may have been changed." with Details and "Export".
- **Website access** rows: time · what happened (the sentence names the site in full, so there is no host column) · "Open task". Outcomes (S16): "Reached api.github.com"; "Allowed, but api.example.com didn't answer"; "Blocked: example.com isn't on the allowed list" (in `text`, like "Blocked from sending …" in Actions: the allowed list working is not a failure) with "Allow this site…" → dialog "Allow Bothy to send data to {host}? This applies to everyone's tasks until you remove it in Settings → Security." [Allow site] [Cancel] (creates the same narrow grant as v0.1).
- Under 640 px: time and actor move into a meta line under the summary.
- Filtered empty: "Nothing matches these filters." + "Clear filters".

---

## 11. Settings

### 11.1 Row pattern

```
Label                                                       [ control (max 320) ]
One line of help, only when it changes what you'd choose.
──────────────────────────────────────────────────────────────────────────────────
```

| Property | Value |
|---|---|
| Layout ≥ 640 px | `grid grid-cols-[minmax(0,1fr)_minmax(0,20rem)] gap-x-6 items-center min-h-14 py-3 border-b border-border` |
| Layout < 640 px | Stacked: label, help, control full width. Switches stay right-aligned on the label line. |
| Label | `text-body` 500 `text`, a real `<label for>` |
| Help | `text-small muted`, at most two lines; most rows have none |
| Section | `text-heading` as `<h2>`, 40 px above (first section 0), 8 px below; no subtitle. Its rows sit on **one group card** (`.list-card`: `rounded-lg border border-border bg-surface shadow-card`; each row padded 16 px, hairlines edge to edge, the last row without one). `SettingsSection bare` leaves the card off where the content is not rows: option lists, tone cards, tables, code, a sentence with a button; such a section wraps the rows it does have in its own `list-card` |
| Saving | Switches, selects and radios save on change; text fields on blur or Enter. While a row saves, "Saving…" (`text-small muted`, `aria-live="polite"`) shows right of its label. After any save the page header says **"Changes saved"** with a moss check for 2 s (D51, §6.2); the row itself says "Saved" for 2 s only while that header is out of view, so one confirmation is always on screen and never two. Errors replace the help under the control in `danger-text`; the value is not saved. |
| Restart needed | Help "Takes effect after a restart." and the restart banner |
| Conflict | "Someone else changed this setting. Reload to see their change." + "Reload" |
| Provenance | Never outside Advanced |

Page title is the group name, no subtitle.

### 11.2 Account (`/settings/account`, everyone)

| Section | Row | Control |
|---|---|---|
| Profile | Name | text |
| | Username | read-only plain text (no border) |
| | Time zone | searchable `Combobox` (cities, as in General); help "Bothy uses it in your chats and for new schedules."; when this browser's zone differs, a second line "This browser uses {Santiago (America)}." + link button "Use it" (saves it). Saves on choosing, through `PATCH /api/me`; a refused zone says "Bothy doesn't know that time zone. Choose one from the list." |
| | Password | secondary "Change password…" → dialog: Current password, New password (Step 1 help text), [Save password]. A wrong current password: "That's not your current password." on its field; the same password again: "Choose a password different from your current one."; locked after too many wrong tries: "Too many tries. Try again in {n} minutes." An account made by Google or GitHub sign-in has no password: plain text "Not set. You sign in with Google." ("with Google or GitHub" when both are connected), no button |
| Sign-in methods | one row per provider an admin turned on, plus connected ones since turned off, each led by the provider's 16 px mark (D48) | "Google · maya@acme.com" + "Disconnect" (`aria-label` "Disconnect Google", no confirm, toast "Google disconnected."); "GitHub · @maya · maya@acme.com"; "GitHub · Not connected" + "Connect GitHub" (opens GitHub, comes back with the toast "GitHub connected."); a connected provider since turned off reads "{maya@acme.com} · Google sign-in is off". The section shows only when there is a row. Failures are a danger StatusLine under the rows: "That {GitHub} account is already connected to someone else here.", "You've already connected a {Google} account. Disconnect it first.", "You didn't finish connecting {Google}.", "Connecting took too long. Try again.", "You were signed out before {Google} answered. Sign in and try again.", "{sam@acme.com} isn't on this Bothy's sign-in list, so it can't be connected. Admins add people in Settings → Security." (the person may be an admin, so it doesn't send them to one), the sign-in page's sentences for an unverified address and organization approval, "{Google} didn't respond. Try again, or ask an admin to check {Google} sign-in."; disconnecting the only way in: "This is how you sign in. Ask an admin for a password before you disconnect it." |
| Appearance | Theme | Segmented: System, Light, Dark |
| Saved keys | — | "Your own keys for connected tools, used instead of the shared ones when Bothy works for you." + secondary "Manage your keys" → `/settings/account/keys` (§11.6) |
| API access | one row per token | name, plain scope ("Can chat and read"), "Last used 2 days ago", "Revoke…"; [Create token] → dialog: Name (at most 64 characters), "What it can do" (checkboxes in plain words; "Chat with Bothy" adds "It can also change or delete your chats, memory, schedules and saved keys."), Expires (select); then the token once in a `CodeBlock` with Copy and, for a token that can chat or use the AI models (what `/v1` takes), example snippets (curl, Open WebUI) under Details |
| | | [Sign out] (secondary) at the bottom |

Accounts don't link to chat apps, and approval requests and notices for accounts stay on the web (Approvals, banners). A person who also talks to Bothy in a chat app is someone in a chat app there, listed in People (§11.5).

### 11.3 General (admins)

| Section | Row | Control | Help / default |
|---|---|---|---|
| Workspace | Time zone | searchable `Combobox` | "New people start with this time zone. The daily spending limit resets at midnight here." / from setup. Each person's schedules use their own zone (Account). |
| | Web address | text, placeholder `https://bothy.acme.com` | "The address your team uses. Links in chat apps point here." |
| | Connection | status text: "Reached securely over HTTPS", "Only reachable from this computer", or `warning-text` "Reached over plain HTTP from another computer" with, directly under it, "Put Bothy behind HTTPS, for example with Tailscale or Caddy, before your team uses it from other computers." (no Details table: it only repeated the headline) | — |
| Spending | Today | a summary row, only while a limit is set: "{$1.20}" in `text-figure` + "of {$5.00} today" in `text-body muted`, "Resets at midnight" at the right (`text-small muted`), then the `ProgressBar` ("Today's spending", value text "{$1.20} of {$5.00}"; `warning` from 80 %, `danger` at 100 %). The amount is `session.today.global_spent_micros`, refreshed on open and when the limit changes; the limit is the Daily limit setting itself (the value the server measures against), so the summary follows a save at once | — |
| | Daily limit | money input with `$` prefix, 2 decimals | "Bothy stops starting new work when everyone's spending reaches this." / $5.00 |
| Bothy | Name | text | "Bothy" |
| | Picture | the current picture (48 px, round, `alt` Bothy's name) + secondary "Change…" (file picker: PNG, JPEG or WebP) + ghost "Use default" while an admin's picture is in use (`GET /api/agent/picture/info` `custom`); the label names no single control, so both buttons point `aria-describedby` at it and the help ("Change…, button, Picture"). Saving says "Saving…" on the pressed button, then the toast "Picture changed." or "Default picture restored."; each chat app that couldn't take it gets one `warning-text` line with the server's `detail` under the help ("Discord only allows a few picture changes an hour. Try again later.", "Telegram isn't connected right now, so its picture wasn't changed. Try again once it's back."). After "Use default", which goes with the admin's picture, a ghost "Try again" stays in its place while some chat app missed the change and sends it again. With the default in use and Telegram or Discord turned on, the ghost button is "Send to chat apps" (the same `DELETE`; the toast "Picture sent to the chat apps." only when some chat app took it), for a bot still wearing an older default or a picture from BotFather. Focus moves to "Change…" only when the ghost button goes. A refused file is the row's error, in the server's words ("That file isn't a picture Bothy can use. Choose another one."); over 5 MB is refused before sending: "That picture is over 5 MB. Choose a smaller one." While Slack is turned on, a note: "In Slack, upload it under App icon in Slack's app settings." + links "Download picture" (`?download=1`) and "Open Slack's app settings ↗" (`https://api.slack.com/apps/{app_id}/general`, or `https://api.slack.com/apps` until Slack has given `app_id`); right after a change it turns `warning-text` with the alert glyph, since Slack still shows the old one. The row is left out when the server has no picture (503); when the picture doesn't load for another reason, the row says "The picture didn't load. Check your connection and try again." with "Try again". | "Shown on Bothy's replies here and in the chat apps." / the built-in picture. "Change…" carries the `upload` icon |
| | Standing instructions | sans `TextArea`, 4 rows, placeholder "Always reply in British English. Our main product is the Acme web app." | "Bothy follows these in every chat." |
| | Personality | secondary "Edit personality" → `/settings/general/personality`: full-width document editor (sans, `text-reading`) for SOUL.md with [Save] [Cancel] and conflict handling as in §9.2 | — |
| Tone (bare) | — | "How Bothy sounds when it replies." (`text-small muted`), then a `radiogroup` of three cards (`grid gap-3 sm:grid-cols-3`), each a `<label>` holding a visually hidden radio: `rounded-lg border border-border-strong bg-surface p-4`, hover `bg-subtle`, chosen `border-accent bg-accent-subtle` with a moss `check` after the title, keyboard focus a 2 px `focus` outline. Title `text-body` 500, help `text-small muted`, then a sample reply under a hairline in `text-small`. Choosing saves `agent.tone`; a failed save puts the choice back and says why under the cards. Locked by the config file: the radios are disabled | Warm and brief |

| Value | Title | Help | Sample |
|---|---|---|---|
| `warm` | Warm and brief | Friendly, short, first names. | “Good news: the release notes are ready for a look.” |
| `direct` | Straight to the point | Just the answer. | “Release notes are ready to review.” |
| `formal` | Calm and formal | Polished, for client work. | “Good morning. The release notes are ready for your review.” |

### 11.4 Models (admins)

```
Models

Main model            Claude Sonnet 4.5 · Anthropic                     [ Change ]
Used for chats and most work.
──────────────────────────────────────────────────────────────────────────────────
Quick tasks model     Claude Haiku 4.5 · Anthropic                      [ Change ]
Used for titles, summaries and check-ins. A cheaper model works well.
──────────────────────────────────────────────────────────────────────────────────
› Backup models

Providers                                                    [ Add a provider ]
Anthropic             Connected · 6 models                              [ Manage ]
Amazon Bedrock        Connected · 3 models                              [ Manage ]
Ollama                Can't reach it right now                          [ Manage ]   danger-text

Refine
Refine answers by default                                                   (off)
Bothy writes several drafts, checks them and sends the best one. Slower, and costs about {m}× more.
How thorough                                                [ Standard ⌄ ]        only when on
```

- **Change** opens a dialog with a `Listbox` of connected models by friendly name, grouped by provider.
- **Backup models** disclosure: help "If a model doesn't respond, Bothy tries these in order." Two ordered lists, "For the main model" and "For quick tasks"; each row: model name, `⋯` with "Move up", "Move down", "Remove"; "Add backup" select.
- **Add a provider** opens `ProviderChooser` in a 560 dialog (the Step 2 content without the outer frame), footer [Cancel] [Add provider] with Cancel on the left at every width. Like setup's Continue, "Add provider" is `aria-disabled` with "Connect a provider to add it." until the selected option's check connects; pressing it saves, closes the dialog and shows the toast "{Provider} connected." Nothing is saved and the dialog never closes on its own.
- Provider rows read "Connected · {n} models" for every provider, Bedrock too; a Bedrock row's region lives in its Manage dialog. Where credentials come from never shows on the row (§11.1).
- A provider whose last call failed reads, from its health state: "Stopped accepting Bothy's key" (`danger-text`) only when it refused the key; "Can't reach it right now" when Bothy couldn't reach it or where its key is kept (`warning-text`), and in `danger-text` once Bothy stops trying it for a minute after three failures in a row, unless the last one refused the key; "Having trouble answering right now" (`warning-text`) for anything else.
- **Manage** (560 dialog): status line "Connected. Bothy can use {n} models here.", counting from the same list as the row so the two agree; "Replace key" (`KeyField`, auto-check); Region (Bedrock); "Details" (last check: context window, latency, raw last error, address, and for Bedrock "Credentials from: {source}", never the value); "Remove provider…" (`danger-text`, confirm). Key providers show no "Credentials from".
- A model without a name from the server reads as one worked out from its id, in pickers, backups and slot rows: "qwen3:4b" → "Qwen3 4B" (§14.2 Models).
- **How thorough:** "Standard (3 drafts)", "Thorough (8 drafts, slower)". Exact drafts, rounds and early stop are in Advanced → Limits.

### 11.5 People (admins)

```
People                                                            [ Add person ]

(MC) Maya Chen          maya          Owner
(SP) Sam Patel          sam           Admin                                   ⋯
(PR) Priya Rao          priya         Member                                  ⋯
(JK) Jordan Kim         jordan        Member · hasn't signed in yet           ⋯
```

- 56 px rows: avatar 32, name `text-body` 500, username muted, role word. `⋯`: Change role · Daily limit… · Reset password… · Turn off account… (`danger-text`). A turned-off account has Turn on account (toast "Account turned on. Their old API tokens stay revoked.") in place of the last two, since a one-time password couldn't sign in. Only the owner changes another admin: an admin sees just Daily limit… on another admin's row. Refusals say the server's reason ("Only the owner can reset an admin's password.").
- **Turn off account…** confirms: "They're signed out everywhere and can't sign in until an admin turns the account back on. Their API tokens stop working for good. Their chats and memory are kept." Their running tasks and open requests stop too.
- **Add person** (560 dialog): Name, Username, Role (Member / Admin, help "Admins can change settings and approve anything."), [Create]. A name the server refuses says so on the field: "Use at most 64 characters, with no invisible ones." Then the same dialog shows: "{Name} can sign in now. Send them these details:" + a `CodeBlock` with the web address, username and a generated one-time password, Copy; "They'll choose their own password when they first sign in." [Done] (S9).
- **Daily limit** dialog, the same for accounts and for people in chat apps: title "Daily limit for {name}"; radios "Use the default ({amount} a day)" (members' default; "no limit" when there is none, as for admins) and "Set a limit", then the "Daily limit" money field; toast "Limit saved."
- Only the owner: "It's just you so far. Add people so they get their own sign-in." + "Add person". When people use Bothy in chat apps (the section below has rows), the sentence is "Only you sign in to Bothy on the web. Add people so they get their own sign-in." instead; it waits for that list, so the wrong one never shows.

**In chat apps** (a section under the accounts, admins, in any mode): the people Bothy answers in Slack, Discord and Telegram (`GET /api/teammates`), approvers first, then by when they were last active, then blocked people. UI copy never says "teammate" (§14.3).

```
In chat apps
People who can approve get approval requests in their direct messages. Changes to
Bothy's settings still need an admin here.

Share of the daily limit for chat apps                          [ 60% (recommended) ⌄ ]
Leaves the rest of Bothy's $5.00 daily limit for the web.

(SP) Sam Patel                                                   (on) Can approve   ⋯
     @sam · Slack
     Active 30 min ago · $0.42 of $2.00 today
(AK) Alex Kim                                                    (off) Can approve  ⋯
     @alexk · Slack
     Active 4 h ago · $1.15 of $2.00 today
(RP) Riley Park                                                  (off) Can approve  ⋯   switch disabled
     @riley · Slack · No longer in Acme
     Active 6 Oct · $0.00 of $2.00 today
(C)  Casey                                                                          ⋯
     Telegram · Blocked
```

- Help (muted) under the heading, as above; it and the Share row are left out while no chat app is connected. When nobody can approve and the list isn't empty: "Nobody can approve in chat apps yet, so requests from chat apps wait for an admin in Approvals."
- After the upgrade that moved people off linked accounts (`team_access`, the first 7 days, until an admin changes anyone here), a neutral `Notice` above the help: "People in chat apps no longer link to Bothy accounts. Check who can use Bothy and who can approve below." It replaces that banner (§4.9).
- **Share** (`SettingsRow`, only when a daily limit is set): "Share of the daily limit for chat apps", select "40%", "60% (recommended)", "80%", "All of it" (another saved value shows as "{n}%"); help "Leaves the rest of Bothy's {amount} daily limit for the web."; saves `budgets.chat_share_pct` (100 for "All of it") with the row's "Saved".
- **Find:** above 20 rows a field "Find someone" filters by name or handle; no match: "No one matches “{q}”."
- **Rows** (56 px): avatar 32 and name (500); `text-small muted` "{@handle · }{App}{ · ID {account id}}{ · No longer in {workspace}}", the account id only for someone without a username who shares a name with another person of the same app ("Test · Telegram" six times can't be told apart otherwise) ("No longer in the server" on Discord, "No longer in the workspace" on Slack without a workspace name); then "Active {relTime} · {spent} of {limit} today" (`tabular-nums`; without a limit "Active {relTime} · {spent} today"). The limit is their own, else the member default.
- **Can approve:** a `Switch` with the visible label (`aria-label` "Can approve: {name}"); for someone no longer in the team it is disabled, and its `aria-describedby` points at the row's own meta line, whose "No longer in Acme" is the reason, visible in the same row (no second line repeating it). Under 640 px the switch gives way to a "Can approve" item in the row's menu (`menuitemcheckbox`, checked when on, disabled with "No longer in Acme" under it), and the meta line ends " · Can approve" for those who can. Toast "Saved."
- **Menu** "Options for {name}": "Daily limit…", "Block…" (`danger-text`); someone no longer in the team adds "Delete their data…". Blocked rows: meta "{App} · Blocked", no switch, menu "Unblock", "Delete their data…".
- **Daily limit** is the accounts' limit dialog (above); it saves `budgets.per_user_daily_usd.<app>:<id>` (null for the default)."
- **Block** confirm: title "Block {name}?", body "Bothy stops answering them in {App} and stops anything it's doing for them. Their chats are kept." Buttons "Block" (danger) and "Cancel". The row updates in place; toast "Blocked." Unblock: toast "Unblocked."
- **Delete their data** confirm: title "Delete {name}'s data?", body "This deletes their chats, files, notes and schedules in Bothy. It can't be undone." (blocked people add "They stay blocked."). Buttons "Delete" (danger) and "Cancel". Toast "Deleted."
- **Errors:** a failed switch or menu change goes back and says "Couldn't save. Try again." under the row; a failed confirm says so in its dialog. The list failing to load: "This list didn't load. Check your connection and try again." with "Try again".
- **Empty:** with no chat app connected, only "No chat app is connected yet." + "Connect a chat app" (secondary, `/settings/connections`), with neither the help nor the Share row. Otherwise "Nobody uses Bothy in a chat app yet." and, muted, "Message Bothy in {the connected apps, “Slack or Telegram”} to appear here."; when Telegram is connected, or Discord while Bothy can't check its members (`member_check` isn't "ok"), it adds "On {Telegram and Discord}, let yourself in from Approvals first." and the secondary button "Open Approvals" (people there wait in Approvals until an admin lets them in, admins included).
- It reloads on `inbox.changed` (someone let in, blocked or no longer in the team, or a change made here), keeping the rows where they are: a reload only adds, updates and drops rows, so nothing moves under the pointer. The server's order applies on the next visit or "Try again".

### 11.6 Security (admins)

```
Security

Needs attention                                                   only when there are findings
The key that protects saved keys is stored next to the database.         [ How to fix ]
Data files can be read by other users on this server.                         [ Fix ]

Approvals
When should Bothy ask?
┌──────────────────────────────────────────────────────────────────┐
│ (•) Careful                                                       │  OptionList
│     Asks before sending anything out, opening a pull request or   │
│     changing a file.                                              │
├──────────────────────────────────────────────────────────────────┤
│ ( ) Balanced                                                      │
│     Asks the first time it contacts a new person or website, and  │
│     before anything that can't be undone.                         │
├──────────────────────────────────────────────────────────────────┤
│ ( ) Hands-off                                                     │
│     Asks only before deleting, deploying or spending money.       │
└──────────────────────────────────────────────────────────────────┘
If nobody answers a chat request            [ Deny after 15 minutes ⌄ ]
If nobody answers a scheduled request       [ Wait up to 24 hours ⌄ ]
Allowed without asking                      4 permissions ›

Sign-in
Who can sign in                             [ acme.com                  ]
One email address or domain per line,       [ pat@gmail.com             ]
like acme.com or pat@gmail.com.
Create accounts on first sign-in                                            (off)
Members of the GitHub organizations you add also get an account. People who already have an account should connect Google or GitHub in Account first.
Google                                      On ›
GitHub                                      Off ›

Running code
On. Code runs in an isolated workspace.                               or, when off:
Off on this server. Bothy can't run commands or scripts.   › How to turn it on
Let code download Python packages                                           (off)   hidden while code is off
Let code download JavaScript packages                                       (off)
Websites Bothy can reach                    12 sites ›

Saved keys                                  6 saved keys ›

Pause Bothy
Stops everything that's running and keeps anything new from starting until someone resumes it.
[ Pause Bothy… ]                                                    secondary, danger-text label
```

- **Needs attention** comes from the security audit (`fix: false`). "Fix" runs that finding's fix and appears only when one exists; "How to fix" opens Details with plain steps. The section and the attention dot disappear when there are no findings. Running code being off is left out while it is off: the Running code section says it, with "How to turn it on". While the locked-keys banner shows, "Saved keys are locked." is left out too, and the Saved keys page drops its own locked row: the banner's Unlock is the one action, and the Saved keys row keeps "Locked ›". Keys a key file locks (`kek_source` not `passphrase`) have no Unlock: the finding reads "Saved keys are locked because Bothy's key file is missing or doesn't match." with "How to fix": "Put the key file Bothy was set up with back on the server, then restart Bothy." Its Unlock dialog, like Saved keys', asks for a first passphrase twice.
- **If nobody answers** options: chats "Deny after 15 minutes", "Deny after 1 hour", "Keep waiting (up to 24 hours)"; schedules "Wait up to 24 hours", "Deny after 1 hour".
- **"How to turn it on"** (code) opens one level only: "Start Bothy with the compose file from the deployment guide, which includes the isolated workspace service, then restart Bothy." and the "Commands" `CodeBlock`. Backend, runtime, the reason and self-test results are in Advanced → Running code, with the limits. The package switches are hidden while code is off, since they can't work.
- **Sign-in** (SPEC §15): one list of who may sign in with Google or GitHub, shared by both. Its help is one line: "One email address or domain per line, like acme.com or pat@gmail.com." That a domain lets in Google Workspace accounts is said on the Google sign-in page, and that other GitHub accounts need their exact address, next to "Organizations (optional)" on the GitHub page. Removing someone stops their next sign-in but doesn't sign them out (turning their account off in People does); that is said in the deployment guide, not under the field. The list is a textarea, one entry per line (commas also work), saved when it loses focus; entries are lower-cased and "@acme.com" reads as the domain. Before sending it the page refuses a public mail domain ("Anyone can get an address at gmail.com. List each person's address instead.") and anything that isn't an address or a domain ("“acme” isn't an email address or a domain. Write it like acme.com or pat@gmail.com."); emptying the list while a provider relies on it says "Google sign-in is on and needs someone on the list. Turn it off first, or keep at least one entry."; leaving GitHub without an exact address or an organization says "GitHub sign-in is on and needs an email address on the list, or an organization. Turn it off first, or keep an address." The switch "Create accounts on first sign-in" creates member accounts for people on the list without one; its help in team mode is "People who already have an account should connect Google or GitHub in Account first.", led by "Members of the GitHub organizations you add also get an account." when GitHub has organizations; outside team mode it is off and disabled with the help "This Bothy is set up for one person, so it can't create accounts. Whoever runs the server can switch it to team use." and the link "How to switch ↗" (the deployment guide). The provider rows read "Off", "On", or "Not working" (`danger-text`, on but its client secret can't be read). In Needs attention the `auth.sign_in` finding reads "{Google} sign-in is on, but its client secret is missing." (How to fix: "Open Security → {Google} sign-in and save the client secret again."), "Saved keys are locked, so {Google} sign-in can't work." with "Unlock", or "GitHub organizations are checked by name only." (How to fix: "Open Security → GitHub sign-in and press Save so Bothy can confirm the organizations.").
- **Google sign-in, GitHub sign-in** (`/settings/security/{google,github}-sign-in`): title "Google sign-in", back to Security. Top: a Notice when the web address can't carry sign-in ("Set Bothy's web address before turning this on. Google sends people back to it after they sign in." or "Bothy's web address must start with https:// for Google sign-in. Put Bothy behind HTTPS, then change the address in General.", both with "Open General"), when the saved secret is gone ("The saved client secret is missing. Enter it again and save.") or when saved keys are locked ("Saved keys are locked, so Google sign-in can't work. Unlock them in Saved keys." + "Open saved keys"). Then the switch "Sign in with Google" (saves on change; disabled, with the reason as help, until the web address, client ID, client secret and someone on the list are in place: "Save the details below to turn this on.", "Add who can sign in on the Security page to turn this on."; GitHub, which a domain doesn't satisfy: "Add an email address on the Security page, or an organization below, to turn this on."; while the web-address notice shows, the switch has no visible help, since the notice says it, and its reason, "Bothy's web address needs to start with https://." for an http address, reaches screen readers only) and a row "Who can sign in · 3 addresses and domains ›" back to Security, with "A domain on the list lets in the Google Workspace accounts on it." under it on the Google page; the GitHub page counts email addresses only, since domains don't let GitHub accounts in ("Who can sign in · 2 email addresses ›", "No email addresses"). Section "Google Cloud" / "GitHub OAuth app": "Redirect URI" / "Callback URL" in a `CodeBlock` with Copy (while the web-address notice shows, only "Appears once the web address starts with https://."), help "Add this to the OAuth client's authorized redirect URIs in Google Cloud." / "Paste this as the authorization callback URL of a GitHub OAuth app.", button "Open Google Cloud ↗" / "Open GitHub ↗"; Client ID (placeholder "1234…apps.googleusercontent.com" / "Ov23li…"); Client secret: "Saved · Replace" or a password field, help "Stored encrypted. Only sent to Google." GitHub adds "Organizations (optional)", help "Members of these GitHub organizations can also sign in. Other GitHub accounts need their exact email address on the list." (saving looks up each new one on GitHub to confirm which organization it is; an unknown one is the field error "GitHub has no organization called {acme-inx}.", GitHub out of reach is "Bothy couldn't reach GitHub to check the organizations. Try again." and GitHub limiting requests is "GitHub is limiting requests from this server right now. Try again in an hour."; any other failure is "Couldn't save. Try again."), with Details: "If an organization restricts OAuth apps, an owner approves Bothy the first time a member signs in. Someone whose invitation is still pending can't sign in until they accept it." [Save] saves, then checks with the provider ("Checking with Google…"): "Saved. Google accepted the client ID and secret." (the first time details that work are saved, with someone on the list, it also turns the switch on: "Saved. Google accepted the client ID and secret, and sign-in with Google is on.") / "Saved, but Google didn't accept the client ID or secret. Copy them again from Google Cloud." / "Saved, but Google doesn't accept this redirect URI. Add it to the OAuth client's authorized redirect URIs." / "Saved, but GitHub doesn't accept this callback URL. Paste it into the OAuth app's settings." / "Saved, but Bothy couldn't reach Google to check the details."; a refused save says "Couldn't save. Try again." with the field messages under Details. Values set on the server are disabled with "This is set on the server and can't be changed here."
- **Allowed without asking** (`/settings/security/permissions`): rows in plain words "Send messages to #eng in Slack · for Sam Patel · given in “Weekly report” · 3 days ago" + "Remove".
- **Websites Bothy can reach** (`/settings/security/websites`): list of hosts (`text-code`) with Remove; [Add website]; "Blocked recently" rows "example.com · from “Login bug” · 2 h ago" + "Allow this site…".
- **Saved keys** (`/settings/security/keys`): rows "Anthropic API key · used by Anthropic · last used 2 h ago" + "Replace" + `⋯` Delete… Values are write-only, never shown. [Add key] dialog: Name, Value (password field, help "Stored encrypted. Only sent to the service that uses it."); a name over 64 characters once stored: "Use a shorter name, up to 64 characters."; a value over 64 KB: "This is too long for a key: keys can be up to 64 KB.". The secret header the MCP dialog saves (`mcp_<server>_header`) reads "Key for {Server}", and "used by" counts every setting that names a key as a word, "Bearer secret:…" and "?api_key=secret:…" included. A connected tool's own page can replace that key too (Replace key… there, §12.4). Removing an MCP server deletes the key its dialog saved ("Bothy loses these actions and deletes its saved key."). When sealed and the locked-keys banner isn't showing: a top row "Saved keys are locked." + [Unlock] (passphrase dialog; with no passphrase chosen yet, `new_passphrase`, it is the blocking screen's "Choose a passphrase" with both fields and "Save passphrase").
- **Your saved keys** (`/settings/account/keys`, everyone, from Account): the same list, dialogs and actions for the person's own keys (`user:<id>` scope; SPEC §2.11), back to Account. Intro (`text-body muted`): "Keys only you use. When a connected tool works for you, it uses your key in place of the shared key with the same name." (only connected tools read personal keys). Rows have no "used by"; the Add dialog's Name help is "Use the name of the shared key the connected tool uses. An admin can tell you what it is."; empty: "No saved keys yet." + "Add one when a connected tool should use your own account." Members never see the locked row or Unlock: a change refused because the keys are locked says "Saved keys are locked right now. An admin needs to unlock them first." and hides Add, Replace and Delete.
- **Pause dialog** (440): title "Pause Bothy?"; body "Everything running stops now, and nothing new starts, including schedules, until someone resumes it. Approvals that are waiting will be cancelled."; [Pause Bothy] (danger) [Cancel] (focused). While paused this section reads the banner's first sentence, "Maya Chen paused Bothy at 10:42." (with nothing known: "Bothy is paused. Nothing new starts until someone resumes it."), with [Resume] (primary).

### 11.7 Advanced (admins)

Opening sentence (`text-body muted`): "Settings for troubleshooting and fine-tuning. Most teams never need these."

| Section | Contents |
|---|---|
| Health check | Runs on open; "No problems found. Last checked 5 min ago." or findings (sentence, Fix when available, Details); [Run again] |
| Configuration file | Effective YAML read-only in a `CodeBlock` with source comments; [Download bothy.ui.yaml] |
| Backup | "Last backup: yesterday at 03:00" · [Back up now] |
| Rules | Policy rules table (When · Bothy does · Why) with Add/Edit dialog; class overrides; "Test a rule" dialog (tool + JSON → "Bothy would ask, because rule wiki-writes matches") |
| Limits | Per-task steps, time and cost; scheduled-task limits; check-in daily cap; rate limits; Refine drafts, rounds and early stop |
| Running code | Backend, runtime, engine, self-test, processes per box, memory, idle timeout, active workspaces |
| Network | Extra host names, shared hosting suffixes, egress caps |
| Model prices | Overrides table (Model · Input per million · Output per million · Edit) |
| Built-in safety rules | The constitution, read-only, sans prose |
| Logs | Log level select |

Here, and only here, a field whose value differs from the default shows `text-small muted` "Changed · Reset to default" (link button), and an env-sourced value shows "Set by the server environment ({ENV_NAME}). Change it there." with the control read-only (`AdvancedField`).

---

## 12. Connections (`/settings/connections`)

### 12.1 Overview

```
Connections

Chat apps                                                             text-heading
Slack          Connected to Acme · 4 people                              [ Manage ]
──────────────────────────────────────────────────────────────────────────────────
Telegram       Not connected                                            [ Connect ]
──────────────────────────────────────────────────────────────────────────────────
Discord        Not connected                                            [ Connect ]

Code
GitHub         Connected to acme · 6 repositories                        [ Manage ]

Tools
Linear         Ready · 12 actions                                        [ Manage ]
──────────────────────────────────────────────────────────────────────────────────
Sentry         Needs review: 2 actions changed                           [ Review ]   warning-text
──────────────────────────────────────────────────────────────────────────────────
Web search     SearXNG                                                   [ Change ]
[ Add an MCP server ]
```

- Rows 56 px: name `text-body` 500 (120 px column), status `text-small muted`, one small secondary button. No icons, no toggles, no pills.
- Each section's rows sit on one group card (§11.1); the Tools card ends before "Add an MCP server". The same holds on the GitHub page (the account row, repositories, deploy environments), a chat app's page (connection, group chats, "When someone new messages Bothy") and a tool's actions.
- Status words: "Not connected", "Connected…" (chat apps: "Connected to {workspace}" or "Connected as {bot}", then " · {n people}" for the people in chat apps who use Bothy there, leaving out blocked people and people no longer in the team), "Can't connect: {plain reason}" (`danger-text`, button "Reconnect"; tools say "Can't start: {plain reason}" like their page), "Needs review: {n} actions changed" (`warning-text`), "Installing…", "Off".
- GitHub: "Connected to {account}" for an app connection, "Connected as {account}" for a token; with no connection but an app Bothy created that isn't installed yet, "Not connected: {app name} isn't installed yet" (`warning-text`, button "Connect").
- "MCP" appears only on the "Add an MCP server" button and inside its dialog.

### 12.2 GitHub (`/settings/connections/github`)

**Not connected:** the primary path has GitHub create an app of Bothy's own (SPEC §14.3), so pull requests and comments come from `@<app>[bot]`, not from someone's account.

```
← Connections
Connect GitHub
Bothy uses this to read your code and open pull requests. It only ever works on its own branches.
[reminders: apps Bothy no longer uses, if any]

Create a GitHub App for Bothy                                         section-title
Pull requests and comments come from Bothy itself, not from someone's account. You choose which repositories it can use.

Where will Bothy work?
┌──────────────────────────────────────────────────────────────────┐
│ (•) An organization                                               │
│     ───────────────────────────────────────────────────────────── │
│     Organization [                     ]                          │  no placeholder
│     The name in its address, like acme in github.com/acme.        │  field help
│     You need to be an owner of it on GitHub.                      │
├──────────────────────────────────────────────────────────────────┤
│ ( ) Your personal account                                         │
│     For repositories your own GitHub account owns.                │
└──────────────────────────────────────────────────────────────────┘
› Change the app's name                                              Disclosure inline: App name [ Bothy (acme) ] (placeholder = the proposed name)
                                                                     "Names are unique on GitHub. You can also change it on the next page."
› What can the app do?                                               "Contents, pull requests, issues and Actions: read and write. Actions is for deploys."
                                                                     "Metadata: read." / "It can't change repository settings, secrets or workflow files."

[ Continue on GitHub ]                                               primary; "Opening GitHub…"; with no organization: "Enter the organization's name."
GitHub opens in this tab. Sign in there if asked, then create the app and install it.

› Other ways to connect                                              Disclosure section
    (•) Personal access token                                        OptionList labelled "How do you want to connect?" (a visually hidden h2) — Quickest. Acts as the person who created the token.
    ( ) A GitHub App you already have — Paste its App ID and private key.
    … token or App ID + private key, "Which repositories can Bothy use?", [ Save ] …
› Using GitHub Enterprise?                                           reveals "GitHub address"
```

- The proposed name is "Bothy ({organization, or your username})", cut to GitHub's 34 characters, or "Bothy (acme) 2" and on when an app Bothy recorded already has that name (the server proposes the same). An organization name that can't be one: "That isn't an organization name on GitHub. Use the name from its address, like acme in github.com/acme." A longer name: "Use 34 characters or fewer."
- Continue on GitHub goes through a Bothy page that posts the app's details to GitHub. Failures under the button: "Open Bothy at {address} to create the app. GitHub sends you back to that address." [Open {address}] / "Bothy's saved keys are locked. Unlock them, then try again." [Unlock saved keys] / otherwise "Bothy couldn't start creating the app. Try again." + Details. If another app is already waiting, the page shows it instead.
- **Waiting to be installed** (replaces the create form; one quiet check on load):
  ```
  Finish installing Bothy (acme) on GitHub                           section-title
  GitHub created the app. Install it on acme and choose the repositories Bothy can use.
  [ Install on GitHub ↗ ]  [ Check again ]                           "Checking…"
  Start over…                                                        danger-text
  ```
  When a member asked for approval: "An owner of acme needs to approve Bothy (acme) on GitHub. Once they have, choose Check again." (in the manage view's waiting block too) Check again: "Bothy (acme) isn't installed yet." or "Bothy couldn't check with GitHub. Try again." + Details; installed opens the manage view with the picker. Start over asks "Start over?" / "Bothy stops using {name} and deletes its keys. The app stays on GitHub until you delete it there." [Cancel] [Start over] (danger, "Starting over…").
- **Apps Bothy no longer uses** (one row each, connect and manage views): "Bothy no longer uses the GitHub App “{name}”. Delete it on GitHub too." (the app's name usually starts with "Bothy", so the sentence says what it is) [Open app settings ↗] "Dismiss" (toast "Dismissed."). Bothy never deletes apps on GitHub itself.
- **Coming back from GitHub** (`?github_app=`, read once and removed from the address): installed → the manage view opens "Which repositories can Bothy use?"; repository access changed on GitHub → Add repositories; otherwise one danger line at the top, the error formula, with [GitHub Apps ↗] when Bothy knows which list holds the new app:
  - "Bothy couldn't finish setting up the app: too much time passed, you signed out, or you started in another browser. Delete the new app on GitHub, then start again."
  - "GitHub created the app, but Bothy couldn't get its keys from GitHub. Delete the new app on GitHub, then start again." (GitHub refused or had expired the code, failed or was out of reach)
  - "Bothy couldn't keep the new app's keys, so it can't use it. Delete the new app on GitHub, then start again." (GitHub handed the keys over; saving them failed)
  - "Bothy's saved keys are locked, so it couldn't keep the new app's keys. Unlock them and delete the new app on GitHub, then start again." [Unlock saved keys]
  - "Bothy couldn't confirm the app's installation with GitHub. Choose Check again, or install it again." (in the waiting block, in either view)
  - "That link to GitHub expired. Start again." / "Bothy can't connect GitHub right now. Try again later."

Under "Other ways to connect":
- One setup path shows at a time. Opening "Other ways to connect" folds the create-app block to one link, "Create a GitHub App for Bothy instead"; that link folds the other path again and moves focus to the "Create a GitHub App for Bothy" heading. What was typed in either is kept.
- "Which permissions?" (token) lists: Contents, Pull requests, Issues and Actions (read and write; Actions is for deploys), Metadata (read).
- A GitHub App you already have: "App ID" and "Private key" (file picker for the `.pem`, or "Paste instead" textarea); then "Installed on acme · 14 repositories" or "The app isn't installed on any organization yet." + [Install on GitHub ↗].
- After Save: toast "Saved." and a `StatusLine` "Bothy is reading 2 repositories. You can keep working; this takes a few minutes. See progress in Knowledge ›", counting only the saved repositories Bothy hasn't read yet (a read that finished or failed doesn't start again), and left out when that is none.
- Failures (S13): "GitHub didn't accept that token. It may have expired or been revoked." / "The token works but can't see any repositories. Give it access to at least one repository on GitHub." / "Bothy couldn't reach {address}. Check the address and that this server can reach it." / per repository: "Bothy can read acme/web but can't open pull requests there. Give the token Pull requests: read and write."

**Connected (manage view):** sections
- **Account:** an app Bothy made: "Connected to acme as Bothy (acme)" with a small muted "Pull requests and comments show as bothy-acme[bot]."; another app: "Connected to acme (GitHub App)"; a token: "Connected as acme-bot (personal access token)". "Replace token" / "Change connection…" (for a token, or an app someone pasted, the connect view opens on "Other ways to connect" with that path chosen, and "Create a GitHub App for Bothy instead" one link away; for an app Bothy made, on the create-app block; a new app made from there replaces the connection once it is installed, keeping the repositories it can reach; until then the waiting block sits at the top: "Finish installing {name} on GitHub to switch Bothy to it."; if the switch fails once it is installed, a danger line at the top: "Bothy couldn't switch to {name} yet, so it still uses this connection." Try again, "Switching to {name}…", and when Try again finds the app no longer installed on its owner, under it "{name} isn't installed on {owner} any more." [Install on GitHub ↗]); "Disconnect GitHub…" (`danger-text`, confirm "Bothy will stop reading these repositories and can't open pull requests or deploy. Project notes are kept.", plus "{name} stays on GitHub until you delete it there." for an app Bothy made). When GitHub stops accepting an app Bothy made: "GitHub stopped accepting {name}. If it was uninstalled, install it again." [Install on GitHub ↗] [Check again] ("{name} isn't installed yet." under the row while it isn't).
- **Repositories:** the picker list with switches, "Read today at 09:12" per row and `⋯` (Read again, View on GitHub); [Add repositories]. The dialog is titled "Which repositories can Bothy use?" while none is in use. It offers only repositories not already on the page: one listed but off is turned back on with its switch, so no repository has two controls. For an app connection it adds "Missing a repository? Give the app access on GitHub ↗" (the installation's settings), or, with nothing left to add, "Bothy already uses every repository the app can reach. Give it access to more on GitHub, then come back." (with some listed but off: "Every repository the app can reach is already listed. Turn one on with its switch, or give the app access to more on GitHub.") [Choose repositories on GitHub ↗]. Back from GitHub after an install, the picker opens only when it has something to add; a switch that kept every repository instead says "Connected. Bothy keeps using {2 repositories}." (toast). Adding shows "Bothy is reading {n} repositories. You can keep working; this takes a few minutes. See progress in Knowledge ›", counting only the added repositories Bothy hasn't read yet (none: no line).
- **Deploy environments:**
  ```
  Deploy environments                                              [ Add environment ]
  production     acme/web · Deploy workflow · 2 approvals from Alex Kim, Priya Rao, any admin    ⋯
  staging        acme/web · Deploy workflow · 1 approval from any admin                          ⋯
  ```
  Empty: "No deploy environments yet. Add one so Bothy can deploy with approval." + "Add environment".
- **Details:** rate limit remaining, last sync, Enterprise address; for an app Bothy made "GitHub App: {name}" and "App settings ↗".

**Environment dialog** (560):

| Field | Control | Default | Help |
|---|---|---|---|
| Name | text | — | placeholder "production" |
| Repository | select of allowed repositories | first | — |
| Deploy workflow | select of the repository's workflows, "Deploy (deploy.yml)" (S19); text field fallback | first containing "deploy" | "The GitHub Actions workflow Bothy starts." |
| Branches | text | `main` | "Bothy can only deploy from these. Separate with commas; * means any." |
| Who can approve | multi-select `Combobox` of people plus "Any admin" (chosen items as a comma-separated line, not chips) | Any admin | — |
| Approvals needed | Segmented 1 / 2 | 1 | With 2: "Two different people must approve, and neither can be the person who asked." |
| › Workflow inputs | key/value rows | none | — |

Buttons: [Save environment] [Cancel]; edit adds "Delete environment…" (`danger-text`).

### 12.3 Chat apps (`/settings/connections/{slack,telegram,discord}`)

**Not connected:** one page with numbered steps (not a wizard, not accordions). Step numbers are `text-heading muted` digits, not circles; step titles `text-heading`. Nobody links a chat account to a Bothy account: once the app is connected, people use Bothy there as people in chat apps (SPEC §5.1).

```
← Connections
Connect Slack

1  Create the Slack app
   Slack opens with everything filled in. Choose your workspace, click Create, then Install to Workspace.
   [ Create the app in Slack ↗ ]

2  Add Bothy's picture (optional)
   In Slack, open Basic Information and upload it under App icon.
   [▣] [ Download picture ]                     the picture as Slack crops it (48 px, rounded)

3  Paste the two tokens
   Bot token            [                                        ]
   Starts with xoxb-. In Slack: OAuth & Permissions → Bot User OAuth Token.
   App-level token      [                                        ]
   Starts with xapp-. In Slack: Basic Information → App-Level Tokens →
   Generate Token and Scopes. Add the connections:write scope.
   ✓ Connected to Acme as @bothy.                     check runs once both are filled

   Message Bothy in Slack to try it. To approve requests there, turn on Can approve
   for yourself in People.                                       "People" links to /settings/people
```

- Slack's step 2 is there because Slack lets only a person set an app's icon, and it lands on Basic Information after Create. Nothing waits on it; the steps reached from "Replace the tokens" leave it out, since the app exists by then. Telegram and Discord bots get the picture from Bothy when they first connect, unless someone already gave them one (SPEC §5.1).
- Telegram: 1 "Create a bot with @BotFather" (send `/newbot`, choose a name, copy the token); 2 "Paste the bot token" (help "Looks like 123456789:AA…", so a bad paste stands out; "✓ Connected as @acme_bothy_bot."); then the try-it line and [Open Telegram ↗] (`https://t.me/{bot}`).
- Discord: 1 "Create a bot in the Discord developer portal" (link; "Create an application, add a bot to it, then turn on Message Content Intent under Bot." and "Under Bot, turn off Public Bot, so only you can add Bothy to servers."); 2 "Paste the bot token and application ID"; 3 "Add Bothy to Discord" with [Invite Bothy to your server ↗]; then the try-it line.
- The try-it line sits under the last step, indented to the step text, once the check passed. Slack: "Message Bothy in Slack to try it. To approve requests there, turn on Can approve for yourself in People." Telegram and Discord: "Message Bothy in {App}, then let yourself in from Approvals. To approve requests there, turn on Can approve for yourself in People." ("Approvals" links to `/approvals`): Telegram can't say who is in the team, and Discord can't before a channel is on, so the admin's own first message waits there like anyone's.
- A Slack app created before its manifest asked for `channels:read` and `groups:read` shows group chats by channel id until it is reinstalled to the workspace (SPEC §8.6); the names arrive with the next message after that.
- Saved tokens show "••••• saved · Replace".
- Failures: "Slack didn't accept those tokens. Check you copied the app-level token and the bot token from the same app." / Discord close 4014: "Discord needs Message Content Intent turned on for this bot. Turn it on in the developer portal under Bot, then try again." / Telegram: "Telegram didn't accept that token. Copy it again from @BotFather."

**Connected (manage view):**

| Section | Rows |
|---|---|
| Connection | "Connected to Acme as @bothy" + [Test connection]; a test the app rejects replaces that line with the danger line "Can't connect: Slack didn't accept the tokens." ("the token" for Telegram and Discord) + "Replace the tokens" and Details, with no second line repeating it; "Turned on" switch; Slack only, the row "Picture": the picture (32 px, rounded) + small secondary "Download picture" (described by the row's label), with "In Slack, upload it under App icon in Slack's app settings." and the link "Open Slack's app settings ↗" under the label (as in General); "Disconnect Slack…" (`danger-text`, confirm: "Bothy stops answering in Slack and forgets its tokens. People and group chat settings are kept.") |
| Group chats | one row per channel: "#eng" with `text-small muted` "{workspace or server · }{n people}" plus " · Shared with other companies" when Slack says so; switch "Answers here" (`aria-label` "Answers here: {chat}"); while Bothy answers there, select "Answers when" (Mentioned / Any message) and select "What Bothy can do here" ("Answer questions" / "Do work, with approval"), both described by the chat's name; a channel that is off shows neither, since they only shape how Bothy answers. Turning off the last channel Bothy answers in on a Discord server asks first: title "Turn off Bothy in {chat}?", body "People who are only in {server} won't be able to message Bothy any more." ("the Discord server" without its name), buttons "Turn off" (danger) and "Cancel" (the switch goes back on). A channel that is off has a small ghost "Delete…" (`aria-label` "Delete {chat}…") on the left, under the meta line, so every row keeps the switch and "Answers here" in the same right-hand column at every width → confirm: title "Delete {chat}?", body "This deletes its chats, files, notes and schedules in Bothy, and what Bothy may do there without asking. If Bothy is still in {chat}, the next message there brings it back to Approvals. This can't be undone.", buttons "Delete group chat" (danger) and "Cancel" (`DELETE /api/spaces/{id}`). |
| Who can use Bothy | Slack: "Everyone in {workspace} can message Bothy and use what it can read, such as your repositories and connected tools. Guests and people from other companies can't. Anything that changes something waits for an approver." ("your workspace" without a name; installed org-wide: "Everyone in your Slack organization, across all its workspaces, can message Bothy …"). Discord: "Members of a server where Bothy answers in a channel can message Bothy and use what it can read, such as your repositories and connected tools. Bots can't. Anything that changes something waits for an approver."; when Discord won't say who is in the servers (`member_check: no_access`), "Bothy can't check who's in your servers, so only people you let in from Approvals can message it."; when Bothy sits in servers with no channel on (`idle_servers`), a muted line "Bothy is in {n} servers where it isn't on in any channel. Remove it from servers you don't use." Both can show at once; the UI writes them from those fields, never from the status's `detail`. Telegram: "People you let in from Approvals." Telegram and Discord add the row "When someone new messages Bothy": select "Ask an admin (recommended)" / "Don't reply or notify anyone", help "Either way, they're listed in Approvals. Applies to Telegram and Discord." All three end with a secondary button "Manage people" → `/settings/people`. |

### 12.4 MCP servers

- **Add an MCP server** (560 dialog): "Name" (placeholder "Linear"), written as people say it: Bothy keeps it lower-cased with spaces as dashes ("Team Wiki" is `team-wiki`, shown back as "Team wiki"), and refuses only other characters ("Use letters, numbers and spaces, like Linear.") or more than 32 ("Use 32 characters or fewer."); "How does it run?" `OptionList`: "A web address (recommended)" / "A command on this server"; web address → "Address", then the key: "Header (optional)" (placeholder "Authorization", the header used when it's empty; only letters, numbers and dashes: "Use letters, numbers and dashes, like X-Api-Key."), a "Key" select while there are saved keys ("Enter a key", then "Saved key: {name}" for each, which sends that saved key as it is, `secret:<name>` or its aws-sm reference, with nothing new saved), and while no saved key is picked a password field "Key (optional)" ("New key (optional)" beside the select; help "Sent as is, such as Bearer abc123. Stored encrypted. Only sent to this address."), whose value goes to saved keys as `mcp_<server>_header`; command → "Package" (e.g. `@linear/mcp`) and "Version (optional)" (empty means the latest; help "Bothy will install version {v} and check it hasn't changed." once looked up). Bothy looks the package up (`POST /api/mcp/resolve`) and pins the exact version and npm's integrity hash, and runs the package's own program (its npm `bin`); the install command and the hash are under Details, never typed. A package npm doesn't know is a field error ("npm has no package called {name}."), and so is one without a program ("This package has no program for Bothy to run."). An address on a private network, as company and container servers are (an IP in a private range, a name without a dot, or one ending in .internal, .local, .localhost, .lan, .home.arpa, .corp or .intranet, such as host.docker.internal), shows the checkbox "Allow {host}" with "It's on your private network. Bothy blocks private addresses unless you allow them." under the Address; adding needs it ticked (otherwise the Address says "Allow {host} below, or Bothy can't reach it.", D15) and writes `allow_private`. A plain http address with a secret header is then accepted, and the server refuses it at connect time if the name turns out to resolve to a public address (the server page then says its key needs an https:// address). An http address with a secret header to a public host is refused: "Use an https:// address: over http the secret header would be sent unencrypted." Then the websites it asks to reach in plain words. [Add server]. Its actions then arrive in Approvals for review.
- When running code is off and the command option is chosen: "This tool runs as a command, and running code is off on this server. Use its web address instead, or turn on running code in Security.", and [Add server] is `aria-disabled` (the notice right above is the reason). In this dialog and on the tool's page "server" means only Bothy's own host.
- **Server page** (`/settings/connections/mcp-<name>`): one status line with the on/off switch at its right (`aria-label` "Turn on {Server}"; there is no separate Server section): "Ready · 12 actions", "Installing…", "Off", or "Can't start: {plain reason}" ("Linear didn't answer" for a web address, "the command stopped with an error" for a command; from the status code: "a saved key it uses is missing" with [Add key], "it asks for a key" with [Add key] (`key_needed`: it refused while it has no key at all, so there is nothing to replace), "it needs a shared key to show what it can do" with [Use my key] for a server that won't list its actions without a key when only people's own keys exist (it first asks, 440 dialog "Use your own key?": "Bothy will send your own key “{name}” to {host} when it checks what {Server} can do. It's never used for anyone else." [Cancel] [Use my key]; then `POST /api/mcp/{server}/use-my-key`: the admin's own key lists them, never makes anyone else's calls; a refusal shows its words under the status line, "You haven't saved your own key named {name}. Add it in Saved keys, or add a shared key."), "Bothy's saved keys are locked" with [Open saved keys], "Linear didn't accept its key" with [Replace key] (Add key and Replace key only for a key Bothy keeps: one in AWS Secrets Manager gets [Try again]), "its address is on a private network" with [Allow {host}] (sets `allow_private`; also for a name that resolves to a private address), "it isn't on your private network, so its key needs an https:// address" for a plain http address with a key whose name turns out to be public, "it needs to be installed" with [Install] for a command never installed or whose installed files changed, where the line used to read "Off" though the switch is on, "it couldn't be installed" with [Install] for one whose install failed; otherwise [Try again]). While Bothy will try again on its own (`retry_at`, a failed install or a server that didn't answer) the line ends "Bothy tries again in {n} min." ("in a moment" once due); the page looks again by itself while it reads "Installing…" or "Connecting…" and when that retry is due. A reinstall of the version a command already runs leaves its actions working: the line reads "Installing…" meanwhile, and if the reinstall fails it stays "Ready · 12 actions" (or the review line) with the warning "Bothy couldn't reinstall it, so it keeps using the earlier install." under it (code `install_failed` without an error state; the raw reason behind Details, [Install], and "Bothy tries again in {n} min." while a retry is due). With actions waiting for review the line is the warning "1 action changed and needs a review before Bothy can use it." + [Review changes] in place of "Ready", matching the overview's "Needs review". While off, Actions reads "Turn it on to see what it can do."; until it runs (installing, connecting, or it can't start) "Its actions show here once it's running."; running with none, "{Server} has no actions." An unknown name gets the title "Tool not found", "This tool was removed or never added." and [Go to Connections]; "Actions" rows: plain name, what it does, "What this does" select (Only reads information / Changes things inside Bothy / Sends data out / Can't be undone), switch, Details (raw schema); an action whose change waits for review has no row until it is reviewed; a save the server refuses because the action changed meanwhile says "This action changed after it was shown. Review its change first."; "Review changes" when definitions changed (same content as the Approvals item; an action whose definition declares `readOnlyHint` adds "{Server} says this only reads information." under its description, while "What this does" keeps the class config or the last review chose, since a server's own word may raise that class but never lower it); `⋯` Reload · Replace key… (a web address server whose key isn't kept in AWS Secrets Manager; "Add a key…" while it has no shared key: it sends none, or the one it names is missing) · Remove server…. Replace key and Add key open one 440 dialog, "Replace the key for {Server}" or "Add a key for {Server}": a password field "Key" (help as in the Add dialog, "Sent as the {Header} header, such as Bearer abc123. Stored encrypted. Only sent to this address." when the key is the whole header; "Enter only the key: Bothy puts “Bearer” before it." in place of the first sentence when the header puts words before the key; else only the last two sentences; empty: "Enter the key."), [Save key] / [Cancel]. While the server names no key, the Add dialog's key fields stand in for "Key" (Header, the saved-key select and "New key", no longer optional), so a saved key can be sent in any header. It writes the shared saved key the server names (from a header, else its address; one typed here while it names none is `mcp_<server>_header`, sent in the header given, else Authorization, as the Add dialog does; a saved key picked is only referenced), then tries it at once (Reload, or the new header rediscovers it); toast "Key saved.". An added key the address can't take is deleted again, and a plain http address to a public host says "Over http the key would be sent unencrypted. Add the server again with its https:// address." People's own keys of the same name still make their own calls. A server removed here and added again is reviewed afresh; one that a `bothy.yaml` edit makes invalid keeps its reviewed actions for when it is fixed.

### 12.5 Web search (`/settings/connections/search`)

`OptionList`: "Off"; "SearXNG" ("Private search you host yourself", Address field); "Brave Search" (API key); "Tavily" (API key). The check runs automatically: "✓ Search works." Failures follow §5.6.4.

---

## 13. Empty, loading, error states and phones

### 13.1 Empty states

One sentence in `text-reading text`, an optional second sentence in `muted`, then at most one button 16 px below. Left-aligned at the column edge, 24 px under the page header. Only the empty chat is a heading. No icon, no illustration; Bothy's logo appears only beside the Approvals → Waiting sentence (`EmptyState picture`, 32 px) and above the empty-chat title (40 px).

| Where | Copy | Action |
|---|---|---|
| Approvals, Waiting | "Nothing needs your approval. When Bothy needs someone's OK, it shows up here.", with Bothy's logo beside it | — |
| Approvals, Done | "No decisions in the last 30 days." | — |
| Knowledge, no GitHub (admin) | "Connect GitHub to give Bothy your code. It reads your repositories, keeps notes on each project and can answer questions about them." | Connect GitHub |
| Knowledge, no GitHub (member) | "Ask an admin to connect GitHub so Bothy can read your code." | — |
| Knowledge, nothing chosen | "Choose which repositories Bothy can read." | Choose repositories |
| Knowledge search | "Nothing in your projects or notes matches “{q}”." | — |
| Memory section | "Bothy hasn't remembered anything yet. It adds things here as you work together, or you can write them yourself." | Edit |
| Skills | "No skills yet. Skills teach Bothy how your team does specific things, like writing release notes." | Add skill (admins) |
| Tasks | "Nothing yet. Tasks Bothy works on, in chats or on a schedule, show up here." | Start a chat |
| Schedules (no schedules of their own and no examples) | "Add your own, like a Monday summary of open pull requests." | — ("New schedule" is always in the header) |
| Releases | "No deploys yet. Ask Bothy in a chat, for example “Deploy main to staging”." | — |
| Spending | "Nothing spent in this period." | — |
| Audit log, filtered | "Nothing matches these filters." | Clear filters |
| Connections → Tools | "No other tools connected. MCP servers let Bothy use other services, like Linear or your database." ("other": the Web search row sits right above it, and may be on) | Add an MCP server |
| Deploy environments | "No deploy environments yet. Add one so Bothy can deploy with approval." | Add environment |
| People (owner only) | "It's just you so far. Add people so they get their own sign-in." ("Only you sign in to Bothy on the web. …" when people use Bothy in chat apps) | Add person |
| People → In chat apps | "Nobody uses Bothy in a chat app yet." + "Message Bothy in Slack or Telegram to appear here. On Telegram, let yourself in from Approvals first." (no chat app connected: "No chat app is connected yet.") | Open Approvals (Telegram or Discord connected) / Connect a chat app |
| Activity → Schedules → In chat apps | (the section is left out) | — |
| Search dialog | "No results for “{q}”." | — |
| Route not found | Title "Page not found"; "Check the address, or go back to your chats." | Go to chat |
| Chat, task or approval not found | "This chat doesn't exist or was deleted." / "This task was deleted, or it belongs to someone else." / "This request was already decided or doesn't exist." | Go to chat / Go to Activity / Go to Approvals |
| Member on an admin page | "Only admins can see this page." | Go to chat |

### 13.2 Loading and errors

- No skeletons. Lists and pages keep their layout and show "Loading…" (`text-body muted`) only after 400 ms.
- Buttons doing work show a spinner and an "-ing" label ("Saving…"), with the width locked.
- Streaming never shows a spinner after the first word.

| Where | Copy | Action |
|---|---|---|
| Page failed to load | "This page didn't load. Check your connection and try again." | Try again |
| Part of a page failed to load | Names the part, never "This page": "The history didn't load. …" (a dialog's list), "The search didn't finish. …", "This file didn't load. …" (a skill file), each ending "Check your connection and try again."; a later page of a list: "Couldn't load more. Check your connection and try again." under Show earlier, with the list kept | Try again (Show earlier stays) |
| Live updates lost | A muted line at the top of the main area: "Reconnecting…"; after 30 s "You're offline. This page will update when you're back."; on reconnect "Connected" fades after 2 s | — |
| Session expired (401) | Redirect to sign in with "You were signed out. Sign in again to continue." | — |
| Forbidden action (403) | Inline: "You can't do this. Ask an admin." | — |
| Settings save failed | Under the control: "Couldn't save. {plain reason or "Try again."}" | — |
| Edit conflict | "Someone else changed this while you were editing." | See their version / Reload |
| Vault sealed (admins, blocking screen) | Title "Unlock saved keys" (every other surface calls them saved keys); text "Bothy can't reach its models or connections until you enter the passphrase."; field "Passphrase". A refusal the passphrase can't fix shows the server's sentence on the field ("A passphrase can't unlock these saved keys: …", or for a first passphrase "This sets the passphrase for the saved keys, so it needs at least 12 characters.") instead of "Try again". Locked by a key file (`kek_source` not `passphrase`): no field, "Bothy can't reach its models or connections: its saved keys are locked because the key file it was set up with is missing or doesn't match, so a passphrase can't unlock them. Put that key file back on the server, then restart Bothy." (Saved keys' locked row says the same, without Unlock). No passphrase chosen yet (`new_passphrase`): title "Choose a passphrase", "Bothy keeps its saved keys locked with a passphrase. Choose one of at least 12 characters and keep it safe: without it, nobody can unlock them.", fields "Passphrase" and "Type it again" ("The two passphrases don't match.", "Use at least 12 characters."), button "Save passphrase" | Unlock |
| Server unreachable | "Bothy isn't responding. It may be restarting." Also when the app opens and `GET /api/auth/me` gets no answer or a 5xx: that screen replaces sign-in, which would wrongly say "You were signed out" and fail the same way | Try again (reloads) |

### 13.3 Phones (360 px) and narrow layouts

```
┌────────────────────────────────────┐   ┌────────────────────────────────────┐
│ [≡] Clean up staging notes       ⋯ │   │ [≡] Approvals                      │
├────────────────────────────────────┤   ├────────────────────────────────────┤
│        ┌─────────────────────────┐ │   │ [Waiting 2]  [Done]                │
│        │ The staging notes are   │ │   │ For you                            │
│        │ out of date. Delete     │ │   │ ┌────────────────────────────────┐ │
│        │ them.                   │ │   │ │ Delete notes/staging.md?       │ │
│        └─────────────────────────┘ │   │ │ This can't be undone.          │ │
│ (◉)                                │   │ │ File                           │ │
│ ◷ Waiting for your approval        │   │ │ notes/staging.md               │ │
│ ┌────────────────────────────────┐ │   │ │ Content                        │ │
│ │ Delete notes/staging.md?       │ │   │ │ 1 line · Show                  │ │
│ │ This can't be undone.          │ │   │ │ [         Delete file        ] │ │
│ │ File                           │ │   │ │ [     Allow for this chat    ] │ │
│ │ notes/staging.md               │ │   │ │ [            Deny            ] │ │
│ │ [         Delete file        ] │ │   │ │ Clean up staging notes ·       │ │
│ │ [            Deny            ] │ │   │ │ Expires in 15 min · Details    │ │
│ │ Expires in 15 min · Details    │ │   │ └────────────────────────────────┘ │
│ └────────────────────────────────┘ │   │ Review changes to Linear tools     │
│ ┌────────────────────────────────┐ │   │ 2 tools changed · 1 h ago          │
│ │ Add to the current task        │ │   └────────────────────────────────────┘
│ │ (📎) (◎ Refine) Sonnet 4.5 ⌄ (■)│ │
│ └────────────────────────────────┘ │
└────────────────────────────────────┘
```

Rules under 640 px:
- 16 px side padding; no horizontal page scroll (`document.scrollingElement.scrollWidth <= 360` on every route). Only code blocks, diffs and wide Markdown tables scroll sideways, inside their own box with a visible scrollbar.
- On coarse pointers controls and rows are 44 px and inputs use 16 px text.
- Card key/value pairs stack (label above value), and the card's type icon is hidden. Card buttons stack full width at 480 px and below. Inbox rows move their one status under line 2.
- Bothy's picture is 20 px on its own line above each of its turns, so replies keep the full width (D49).
- There is no inset panel: the page is `bg` edge to edge and the sign-in card becomes a plain column.
- Screenshots review phones at 375 × 812; nothing may scroll sideways at 360.
- Form and setup primary buttons are full width.
- The composer stays above the on-screen keyboard: `interactive-widget=resizes-content` in the viewport meta, with a `visualViewport` fallback; `padding-bottom: max(16px, env(safe-area-inset-bottom))`.
- Dialogs: 440 px dialogs stay centred with 16 px margins and stacked buttons (primary on top); 560 and 720 dialogs become full-screen sheets with a 56 px header bar (Cancel left, title, primary right), its buttons centred in the bar 12 px from its edges. Drawers become full screen.
- No keyboard hints, no hover-only actions (`⋯` and message actions always visible).
- Long values that matter (commands, paths and hosts on cards, setup commands, schedule names) wrap with `overflow-wrap: anywhere`; truncation only for list titles, which always have a `title` tooltip and the full value on their detail page.
- Every list is a `ListRow`; below 640 px meta folds into one muted line under the title joined with " · ".
- Settings: `/settings` is a list page; each group has "← Settings".

---

## 14. Copy guide

### 14.1 Voice

Bothy's interface sounds like a capable colleague at your company: plain, direct and specific. It says what is happening and what you can do, then gets out of the way.

- **Who we write for:** someone at a company who wants work done, often not an engineer.
- **Plain and specific:** real names, real files, real amounts. "Delete notes/launch-checklist.md?" beats "Confirm action?".
- **Calm:** no exclamation marks, no "Oops", no reassurance nobody asked for. Security shows through good defaults and clear cards; a security fact is said once, where it matters (for example as field help where a key is entered).
- **Short:** two sentences at most in any message, then Details.
- **For a company:** teams, projects, repositories, people, "this server". Never "household", "your machine", "your own assistant", "plan a calm week".
- **Pronouns:** the interface calls the product "Bothy" (it) and the reader "you". It never says "I" or "we". Bothy's own chat replies are model output and may say "I"; so do the hello someone in a chat app gets the first time ("Hi Maya, I'm Bothy. Ask me anything, for example …", SPEC §5.1) and the welcome chat's message ("Hi Maya, I'm Bothy. I work for your team: …", D43), which speak as Bothy like its replies. Those are the only places.

### 14.2 Mechanics

| Topic | Rule | Example |
|---|---|---|
| Case | Sentence case everywhere: titles, buttons, tabs, menus, labels. Proper nouns keep theirs. | "Add an MCP server", "Connect GitHub" |
| Punctuation | Full stop at the end of sentences (help, errors, empty states, toasts). None on labels, buttons, headings or list titles. | "Saved." / "Daily limit" |
| Contractions | Use them. | can't, don't, you're, isn't |
| Apostrophes and quotes | Straight apostrophe `'` in UI strings. Curly double quotes “ ” only around things the person named or typed: chat titles, search terms, schedule names, quoted reasons. | From “Login bug” |
| Ellipsis | The single character "…", only for in-progress states and for buttons or menu items that open a dialog asking for more. | "Saving…", "Pause Bothy…" |
| Numbers | Numerals, locale grouping, correct plurals. | "1 file", "3 files", "1,204 entries" |
| Money | `Intl.NumberFormat` USD. Above 0 and under $0.01: "<$0.01". Under $1,000: two decimals. $1,000 and up: whole dollars. Local models: "Free". Unknown price: "Price not set". Micro-dollars never appear. | "$0.07", "$12.40", "$1,240" |
| Estimates | "about" + rounded up: one significant figure under $1, nearest $0.10 above. | "about $0.20", "about $1.30" |
| Relative time | "just now" (< 1 min), "5 min ago", "2 h ago" (same day), then "Yesterday at 14:02", "14 Oct", "14 Oct 2025" outside the current year. 12 h or 24 h follows the browser locale. Time zones are named only where they matter (schedules, limits). | |
| Durations | "Took 3 s", "Took 2 min 10 s", "Took 1 h 5 min". Never "0 ms". | |
| Countdowns | "Expires in 14 min", "Expires in 2 h", "Expires in less than a minute". Never "14:57". | |
| People | Display name; username only as a fallback; "You" for the viewer in meta lines. | "Sam Patel asked in #eng on Slack." |
| Models | Friendly name; provider as secondary text only when needed. | "Claude Sonnet 4.5", "Qwen3 14B · Ollama" |
| Exact identifiers | Paths, hosts, branches and commands are shown verbatim in mono through `Verbatim`, never paraphrased and never truncated inside approval cards. | `notes/launch-checklist.md` |
| Ids | Never in the main flow; only in Details, Technical details, Raw record and Advanced, each with Copy. | |
| Buttons | Verbs naming the result; verb + object when the context is ambiguous. Never "OK", "Submit", "Yes". | "Create account", "Delete file", "Save" |
| External links | End with ↗ and open in a new tab. | "View on GitHub ↗" |
| Toasts | Past tense, five words or fewer, full stop. Never errors. | "Saved.", "Copied.", "Chat deleted." |
| Disabled controls | Always say why, next to the control. | "Connect a model to continue." |
| Destructive confirmations | Title "Delete {thing}?"; body states the consequence and ends "This can't be undone." when true; buttons "Delete {thing}" (danger) and "Cancel". | |

Never use: exclamation marks, emoji, "Oops", "Whoops", "simply", "just" as filler ("just now" is fine), "easily", "seamless", "magic", "AI-powered", "supercharge", "please" (except when asking someone outside the product to act), "error:" as a prefix, status codes in a main line, all-caps words.

### 14.3 Words kept out of the main flow

"Main flow" is everything except Details, Technical details, Raw record and Settings → Advanced, where these words are allowed. "Token" is fine when it means an access token a person pastes or creates (GitHub, Slack, Telegram, API access); it is never used for model usage.

| Don't say | Say instead |
|---|---|
| token, tokens, tokens in/out (LLM) | nothing; show money. If length matters: "This chat is getting long." |
| context window, context, "33K context" | "how much of the conversation Bothy can keep in mind" (only when it matters) |
| micro-dollars, `$0.0072` | "<$0.01", "$0.07" |
| model id (`claude-sonnet-4-5-20250929`, `custom/mock-model`, `us.anthropic.…`) | "Claude Sonnet 4.5" |
| config key (`sandbox.backend`, `allow_private`, `BOTHY_LOG__LEVEL`), YAML | the effect and where to change it: "Running code is off on this server." |
| provider (as a bare noun) | the provider's name: "Anthropic" |
| tool, tool call, `fs_delete`, `use_tool`, `repo_open_pr` | the action: "delete a file", "open a pull request" |
| run, run id | "task" |
| session | "chat" |
| principal, space | "person", "group chat" |
| teammate (a person in a chat app whom Bothy accepts) | "people in chat apps", "someone in a chat app", or their name. "Teammate" is a word for code and docs only; the copy check refuses it in the UI. |
| chat approver, `can_approve` | "Can approve", "someone who can approve in a chat app" |
| team-owned principal, person space, channel space | "people in chat apps", "a channel" or "a group chat" |
| membership verdict, outsider, admitted | "in the team", "no longer in {workspace}", "let in" |
| grant | "permission", "allowed without asking" |
| egress, allowlist | "websites Bothy can reach" |
| sandbox, box, runsc, runc, gVisor | "running code", "isolated workspace" |
| trifecta, exposed, taint, P·U·S | "this chat has seen private information and content from outside" |
| untrusted content | "content from outside your team (web pages, repositories, other people)" |
| LLM, inference | "AI model" |
| TTFT, latency, ms | nothing; durations in seconds where useful |
| cache hits | nothing |
| slot, Main/Fast, fallback chain | "Main model", "Quick tasks model", "backup models" |
| heartbeat, HEARTBEAT.md | "check-in", "check-in list" |
| SOUL.md, persona | "personality" |
| USER.md, MEMORY.md | "About you", "Things Bothy remembers" |
| TEAM.md, team memory | "Remembered for everyone" |
| constitution | "built-in safety rules" (Advanced only) |
| vault, KEK, sealed | "saved keys", "the key that protects saved keys", "locked" |
| MCP (outside Connections) | "connected tool", or the server's name |
| MCP stdio | "runs as a command" |
| cron, cron expression | "repeat rule" (Custom schedules only) |
| policy preset | "When should Bothy ask?" (Careful, Balanced, Hands-off) |
| side-effect class (`local_write`, `external_send`, `irreversible`) | "changes files", "sends data out", "can't be undone" |
| verdict, auto, rule `default:local_write` | "Why Bothy asked: …" (Details) |
| Allow once / Allow for this session | the verb ("Delete file"), "Allow for this chat" |
| Irreversible | "This can't be undone." |
| kill switch, Stop all | "Pause Bothy" |
| Fork clean | "Continue in a clean chat" |
| compaction | "older messages were summarised" |
| workspace (files) | "Bothy's files" |
| household, single/multi mode | "team", "add people" |
| "your machine", "your own assistant" | "this server", "Bothy" |
| setup token, claim | "setup code", "setup link" |
| Base URL, OpenAI-compatible URL | "server address" |
| interactive (usage bucket) | "chats" |
| raw error strings | the error formula (§14.4) |

"Client ID", "client secret", "Redirect URI", "Callback URL" and "OAuth" appear only on the Google sign-in and GitHub sign-in pages (§11.6), where admins paste values between Bothy and the provider's console and must recognise the console's words.

### 14.4 The error formula

**What happened. What to do.** Then at most one action and a "Details" disclosure with the raw message, verbatim, with Copy.

- Name the party at fault when it isn't Bothy: "Anthropic had a problem answering."
- Never show lowercase server strings ("invalid username or password"), stacked parentheses (".)."), Go error chains or config keys outside Details.
- Never repeat an error in two places. Errors appear next to what caused them and are never toasts.
- No blame ("You entered an invalid…") and no apology loops.

### 14.5 Examples, before and after

| # | Where | v0.1 | v0.2 |
|---|---|---|---|
| 1 | Setup intro | "Welcome to your own assistant" | "Create your account" / "You'll be the owner of this Bothy. You can add your team when you're done." |
| 2 | Setup code error (`02`) | "invalid or expired setup token" | "That code didn't work. It may have expired, so run the command below to get a new one." |
| 3 | Model check passed (`09`) | "✓ key valid ✓ tool calling 33K context 0 ms test cost $0" | "Connected. Bothy will use Claude Sonnet 4.5." |
| 4 | Model check failed (`08`) | `Could not reach 127.0.0.1:9: Post "http://…": egress: dial tcp … allow_private …` | "This server is on your private network. Allow Bothy to reach it?" [Allow 127.0.0.1:9] + Details |
| 5 | Continue disabled (`06`) | greyed button, no reason | "Connect a model to continue." |
| 6 | Done screen (`16`) | "Security check: 3 things to look at" | "Bothy is ready" (findings go to Security → Needs attention) |
| 7 | Global banner | "No sandbox: shell, python and MCP stdio are off. Use the compose file." | no banner; Security: "Off on this server. Bothy can't run commands or scripts." |
| 8 | Top bar | "$0.0072 / $5" | removed; Spending: "Today: <$0.01 of the $5.00 daily limit" |
| 9 | Tool row (`26`) | "Used use_tool" | not shown |
| 10 | Tool row before approval (`26`) | "Deleted notes/launch-checklist.md" | "Waiting for approval to delete notes/launch-checklist.md" |
| 11 | Approval title (`26`) | "Irreversible · Delete file · TOOL fs_delete · PATH notes/staging.md" | "Delete notes/staging.md?" / "This can't be undone." |
| 12 | Approval buttons (`26`) | "Allow once / Allow for this session / Deny" | "Delete file / Allow for this chat / Deny" |
| 13 | Approval countdown | "14:57" | "Expires in 15 min" |
| 14 | Approval context | "Web · session "ses_01m4wa58sk31wa65hetdz7py5d"" | "From “Clean up staging notes”" (link) |
| 15 | Denied in transcript (`27`) | the card disappears | "You denied deleting notes/staging.md · 10:41" |
| 16 | Model error (`33`) | "The model could not answer (…).). Press Retry…" + "model error: …" | "Anthropic had a problem answering." [Try again] |
| 17 | Streaming (`32`) | "Thinking…" under streaming text | removed once text streams |
| 18 | Chat list (`20b`) | "New chat" ×3, "never", "Untitled chat" | first words of the message until the title arrives; empty chats aren't stored |
| 19 | Context drawer (`34`) | "2.3K of 33K tokens · 7%" | nothing; above 70 %: "Getting long. Bothy will summarise older messages soon." |
| 20 | Composer (`23`) | "Message Bothy… (/ for commands)" + "Enter to send · Shift+Enter for a new line" | "Message Bothy" |
| 21 | Usage (`46`) | "usr_01m4w9fm9npqyjega8dcyp…" | "Sam Patel" |
| 22 | Usage chart (`46`) | "Spend by day (UTC days)" | chart labelled in the workspace time zone |
| 23 | Settings (`60`) | "Mode: Household or team · UI · you · 5 min ago · Reset" | removed; team mode is on from setup |
| 24 | Settings (`62`) | "Telegram · disabled · since 7 min ago · disabled · [toggle] · Set up" | "Telegram · Not connected · Connect" |
| 25 | Advanced (`72`) | "Run a check to see findings." | "No problems found. Last checked 5 min ago." |
| 26 | Schedule form (`43`) | "Cron expression" | "When: Every weekday at 9:00" ("Repeat rule" only under Custom) |
| 27 | Login error (`81`) | "invalid username or password" | "That username and password don't match." |
| 28 | Network log (`50`) | "allow · connect failed" | "Allowed, but api.example.com didn't answer" |
| 29 | Memory (`40`) | "339 / 4,096 bytes" | hidden; near the cap: "This is almost full. Remove anything Bothy no longer needs." |
| 30 | Step limit | "Continue (+10 steps)" | "Bothy paused after 10 steps to check in." [Keep going] |
| 31 | Key field help | "Keys are encrypted and only sent to their provider." (under the heading) | field help, once: "Stored encrypted. Only sent to Anthropic." |
| 32 | Starter prompt | "Help me plan a calm, productive week" | "What can you do for our team?" |

---

## 15. Components

### 15.1 Where they live

- Primitives: `web/src/components/ui/` (new). Domain components: `web/src/components/{shell,chat,approval}/` and next to their screens.
- Svelte 5 with runes, typed props, Tailwind utilities from §3. No `style="…"` attributes (CSP has no `unsafe-inline`; use classes or `style:` directives, which go through CSSOM). No new npm dependencies. `Verbatim`, `Markdown`, `QRCode` and `Pairing` are kept (restyled).
- Deleted by the end of C10: `SourceChip.svelte`, `StackedBar.svelte`, `SandboxStatus.svelte`, `ChainEditor.svelte`, `PresetPicker.svelte`, `ProviderSetup.svelte`, `ConfigField.svelte`, `Section.svelte`, `Empty.svelte`, `Toasts.svelte`, the old `Dialog.svelte`, `DiffView.svelte`, `ApprovalCard.svelte`, `Shell.svelte`, `ChannelSetup.svelte`; `.chip*`, `.stat`, `.alert-*`, `shadow-xs` and `backdrop-blur` in `app.css`; `risk`, `riskBar`, `verdictChip`, `runStatus` classes and `originIcon` in `lib/labels.ts`.

### 15.2 Class recipes (`@layer components` in `app.css`)

As they ship. The Friendly changes: `shadow-primary` on the primary button (none when disabled), a focus border plus halo on inputs with a transparent outline for forced colours, `.card` at 14 px with `border` (not `border-strong`), 20 px padding and `shadow-card`, the new `.list-card` group card, and tabs on a track with a raised thumb.

```css
@layer components {
  .btn {
    @apply inline-flex h-9 shrink-0 cursor-pointer items-center justify-center gap-2 rounded-md px-4 text-body font-medium whitespace-nowrap transition-colors duration-120 select-none pointer-coarse:h-11;
  }
  /* Repeats the coarse size: as a later rule, h-8 would otherwise win on touch too (§16, 44 px targets). */
  .btn-sm {
    @apply h-8 px-3 pointer-coarse:h-11;
  }
  .btn-primary {
    @apply bg-accent text-on-accent shadow-primary hover:bg-accent-hover active:bg-accent-active;
  }
  .btn-secondary {
    @apply border border-border-strong bg-surface text-text hover:bg-subtle active:bg-hover;
  }
  .btn-ghost {
    @apply text-text hover:bg-subtle active:bg-hover;
  }
  .btn-quiet {
    @apply text-muted hover:bg-subtle hover:text-text;
  }
  .btn-danger {
    @apply bg-danger text-on-accent hover:bg-danger-hover;
  }
  .btn-danger-text {
    @apply text-danger-text hover:bg-danger-subtle;
  }
  .btn-icon {
    @apply w-9 px-0 pointer-coarse:w-11;
  }
  /* IconButton size sm (§15.3 row 2) is 32 px square. */
  .btn-sm.btn-icon {
    @apply w-8 pointer-coarse:w-11;
  }
  .btn[aria-disabled='true'] {
    @apply cursor-not-allowed border-transparent bg-subtle text-faint shadow-none hover:bg-subtle;
  }
  /* A quiet button stays unfilled when disabled, so it never looks pressed (the paused composer's attach). */
  .btn-quiet[aria-disabled='true'] {
    @apply bg-transparent hover:bg-transparent;
  }

  .input {
    @apply block h-9 w-full min-w-0 rounded-md border border-control bg-surface px-3 text-body text-text placeholder:text-faint focus:border-focus focus:shadow-focus focus:outline-2 focus:outline-offset-0 focus:outline-transparent aria-[invalid=true]:border-danger-text disabled:border-border disabled:bg-subtle disabled:text-faint pointer-coarse:h-11;
  }
  textarea.input {
    @apply h-auto min-h-20 py-2 text-reading;
  }
  select.input {
    @apply appearance-none pr-8;
  }

  .field-label {
    @apply block text-body font-medium text-text;
  }
  .field-help {
    @apply mt-1 text-small text-pretty text-muted;
  }
  .field-error {
    @apply mt-1 flex items-start gap-1.5 text-small text-danger-text;
  }

  .card {
    @apply rounded-lg border border-border bg-surface p-5 shadow-card max-sm:p-4;
  }
  /* A group of rows on one card (§11.1): the rows keep their hairlines, which run edge to edge.
     A `contents` wrapper (kept for :last-child or a ref) passes the padding through to its rows. */
  .list-card {
    @apply rounded-lg border border-border bg-surface shadow-card;
  }
  .list-card > :not(.contents),
  .list-card > .contents > * {
    @apply px-4;
  }
  .row {
    @apply flex min-h-10 items-center gap-3 border-b border-border py-2 last:border-b-0 pointer-coarse:min-h-12;
  }
  .row-link {
    @apply -mx-2 rounded-md px-2 hover:bg-subtle focus-visible:outline-offset-[-2px];
  }
  /* In a group card a row that opens something hovers edge to edge, as the empty chat's starters do;
     the card is `overflow-hidden`, so its corners round the first and last row. */
  .list-card .row-link {
    @apply -mx-4 rounded-none px-4;
  }
  /* A row whose `.stretch-link` covers it, its own controls lifted above (`relative z-10`). Only that link's
     focus rings the row: a control of its own (Review, a GitHub link) draws its own ring, never two at once. */
  .row-stretch {
    @apply relative hover:bg-subtle has-[.stretch-link:focus-visible]:outline-2 has-[.stretch-link:focus-visible]:-outline-offset-2 has-[.stretch-link:focus-visible]:outline-focus;
  }
  .stretch-link {
    @apply after:absolute after:inset-0 focus-visible:outline-none;
  }

  .tabs {
    @apply inline-flex max-w-full gap-0.5 overflow-x-auto rounded-lg bg-subtle p-1 [scrollbar-width:none];
  }
  .tab {
    @apply inline-flex h-8 items-center rounded-md px-3 text-body font-medium whitespace-nowrap text-muted hover:text-text aria-[current=page]:bg-raised aria-[current=page]:text-text aria-[current=page]:shadow-raised pointer-coarse:h-11;
  }

  .link {
    @apply text-accent-text underline decoration-accent-text/40 underline-offset-2 hover:decoration-accent-text;
  }
  .page {
    @apply mx-auto w-full max-w-3xl px-6 pt-10 pb-16 max-sm:px-4 max-sm:pt-6;
  }
  .page-title {
    @apply text-title text-text;
  }
  .section-title {
    @apply text-heading text-text;
  }
  .meta {
    @apply text-small text-muted tabular-nums;
  }
  .kbd {
    @apply inline-flex h-5 min-w-5 items-center justify-center rounded-sm border border-border px-1 font-mono text-caption text-muted;
  }
}

/* Unlayered, so it beats a row's own border-b utility: the card's edge ends the last row. */
.list-card > :last-child,
.list-card > .contents:last-child > :last-child {
  border-bottom-width: 0;
}
```

### 15.3 Inventory

| # | Component | Replaces | Props (Svelte 5) | States and behaviour |
|---|---|---|---|---|
| 1 | `Button` | `.btn*` | `variant: 'primary'\|'secondary'\|'ghost'\|'quiet'\|'danger'\|'danger-text' = 'secondary'`, `size: 'md'\|'sm' = 'md'`, `icon?` (16 px; the `google` and `github` marks at 18), `loading?`, `loadingLabel?`, `href?` (renders `<a>`), `download?` (with `href`: the browser saves the file; never routed in the app), `type = 'button'`, `disabled?`, `reason?` (sets `aria-disabled` and links the visible reason by id), `onclick?`, `children` | rest, hover (120 ms), pressed, focus ring, loading (spinner replaces icon, label → `loadingLabel`, width locked, `aria-busy`), disabled (`aria-disabled`, no opacity trick). One primary per view. |
| 2 | `IconButton` | `.btn-icon` | `icon`, `label` (required: `aria-label` + tooltip), `variant = 'ghost'`, `size: 'md'\|'sm'`, `pressed?` | 36 / 32 px, 44 on coarse; `aria-pressed` when `pressed` is set |
| 3 | `LinkButton` | `.link` on buttons | `onclick`, `children` | inherits size, no padding; disabled `text-faint no-underline` |
| 4 | `TextField` | `.input`, `.label`, `.help` | `label`, `value` (bindable), `type?`, `help?`, `error?`, `optional?` (adds " (optional)" muted), `placeholder?`, `prefix?` ("$"), `labelAction?: Snippet`, `trailing?: Snippet` (a small control inside the field's end, such as the password eye; the input gets `pr-10`), `readonly?`, `disabled?`, `autocomplete?`, `inputmode?` | label → 8 px → control → 4 px → help or error. Error replaces help, sets `aria-invalid`, `aria-describedby`. Errors show after first submit, then live. Read-only renders plain text. |
| 5 | `TextArea` | `textarea.input` | as TextField + `rows = 3`, `maxRows?`, `code?` (mono, for YAML and repeat rules) | sans `text-reading`, auto-grow (`field-sizing: content` with a JS fallback) |
| 6 | `KeyField` | part of `ProviderSetup` | TextField props + `check: (value, signal) => Promise<CheckResult>`, `minLength = 20`, `checkingLabel` | idle (help link) → checking (spinner + label) → connected (check + sentence, optional "Change") → failed (`StatusLine` danger + action + Details). Triggers: paste, Enter, 800 ms idle. New value aborts the pending check. Write-only secrets show "••••• saved · Replace". |
| 7 | `Select` | native selects | `label`, `value` (bindable), `options: {value, label, description?}[]`, `help?`, `error?` | native `<select class="input">`, 16 px chevron icon in a wrapper (`pointer-events-none`), friendly names only |
| 8 | `Combobox` | — | `label`, `value` (bindable), `options`, `multiple?`, `placeholder?` | `role="combobox"`, `aria-expanded`, `aria-activedescendant`; filtered listbox styled like Menu; "No matches" row; multiple shows the choice as a comma-separated line under the field (not chips). Used for time zones, people, repositories. |
| 9 | `Listbox` | header model dropdown | `value` (bindable), `options: {value, label, description?, group?}[]`, `trigger: Snippet` | popover `shadow-overlay`, max height 320, items 32 px; ↑ ↓ Home End, typeahead, Enter, Esc; `role="listbox"` |
| 10 | `Menu` | ad hoc menus | `trigger: Snippet`, `items: ({label, onselect, danger?, disabled?, reason?, checked?, toggle?, group?} \| 'separator')[]`, `placement?` | `bg-surface border border-border rounded-lg shadow-overlay p-1 min-w-48`; items `h-8 px-2 rounded-md text-body` (44 coarse), active `bg-hover`; checked shows a check in a 16 px gutter; danger `text-danger-text`; disabled shows its reason as secondary text; arrows, Home/End, typeahead, Esc returns focus; flips to stay on screen; `role="menu"`, `menuitem`/`menuitemradio` (`menuitemcheckbox` for a `toggle`: a setting on its own, like "Can approve") |
| 11 | `Switch` | `.switch` | `checked` (bindable), `label` (visually hidden when a row label exists), `disabled?`, `reason?` | `<button role="switch" aria-checked>`; track 36 × 20, thumb 16, inset 2; off `bg-control`, on `bg-accent`, thumb `on-accent`; 120 ms |
| 12 | `ToggleButton` | — | `pressed` (bindable), `label`, `icon?`, `tooltip?` | `btn btn-quiet btn-sm`; pressed `bg-accent-subtle text-accent-text`; `aria-pressed` (Refine) |
| 13 | `Checkbox`, `Radio` | native | `checked`/`group` (bindable), `label`, `description?` | native inputs, 16 px, `accent-color: var(--color-accent)`; label gives a 32 px hit area (44 coarse) |
| 14 | `OptionList` | `PresetPicker`, provider tiles | `name`, `options: {value, title, description?, disabled?, reason?}[]`, `value` (bindable), `expanded?: Snippet<[value]>`, `framed = true` | one `.card` with `p-0`, hairline rows `min-h-14 px-4 py-3`, radio + title (`text-body` 500) + description (`text-small muted`); selected row's `expanded` renders inside it after a hairline, indented to the title; native radio group keyboard; `framed=false` inside dialogs |
| 15 | `Segmented` | ad hoc | `options: {value, label}[]`, `value` (bindable), `label` | `role="radiogroup"`, a track `h-10 rounded-lg bg-subtle p-1`; options `rounded-md text-small font-medium text-muted`; selected `bg-raised text-text shadow-raised`; 44 on coarse |
| 16 | `Tabs` | `.tabs` | `tabs: {label, href, count?}[]` | route links on a track with a raised thumb (§6.2), `aria-current="page"`; count as muted text after the label; horizontal scroll on phones |
| 17 | `Disclosure` | scattered `<details>` | `label = 'Details'`, `open` (bindable), `variant: 'details'\|'inline'\|'section'`, `children` | button with a 12 px chevron rotating 90°, `aria-expanded`, `aria-controls`; content animates `grid-template-rows`; nesting allowed |
| 18 | `Dialog` | `Dialog.svelte` | `open` (bindable), `title`, `size: 'sm'\|'md'\|'lg' = 'sm'` (440/560/720), `initialFocus: 'first'\|'cancel'`, `dirty?`, `children`, `footer: Snippet` | native `<dialog>` + `showModal()`; `bg-surface border border-border rounded-lg shadow-overlay`; header `px-5 pt-5` title `text-heading` + close IconButton; body `px-5 py-4`; footer `px-5 pb-5` right-aligned, Cancel left of primary; scrim, no blur; Esc closes; scrim click closes only when not `dirty`; nothing closes while a request is in flight; phone rules §13.3 |
| 19 | `Drawer` | context and pins drawers, mobile nav | `open` (bindable), `title`, `side: 'right'\|'left' = 'right'`, `width = 400` | `shadow-overlay`, scrim, focus trap, Esc, returns focus; full screen under 640 px |
| 20 | `Toast` | `Toasts.svelte` | `toast(text, {action?, duration = 4000, or 8000 with an action})` | `bg-text text-bg rounded-lg px-4 py-2.5 text-body shadow-overlay`; one at a time, bottom-left of the main area (16 px), above the composer on phones; pauses on hover/focus; `role="status"`; never errors |
| 21 | `Banner` | banner markup | `tone: 'warning'\|'danger'\|'neutral'`, `text`, `action?`, `dismissible?` | §4.9; `role="status"`, danger uses `role="alert"` on first appearance only; chosen by the banner store |
| 22 | `Notice` | `.alert-*` | `tone: 'neutral'\|'warning'\|'danger'`, `text`, `action?` | inline block `rounded-md px-3 py-2 text-body`, `bg-subtle` / `bg-warning-subtle` / `bg-danger-subtle`, glyph for warning and danger; no success notice |
| 23 | `StatusLine` | test results | `tone: 'progress'\|'success'\|'warning'\|'danger'`, `text`, `details?`, `action?` | 16 px glyph column (spinner, check `accent-text`, alert), text `text-body` (`text` / `warning-text` / `danger-text`), action as LinkButton, Details with a CodeBlock; `role="status"` for progress and success, `role="alert"` for danger only after a person's action |
| 24 | `Card` | `.card` | `padding: 'md'\|'lg'`, `children` | 14 px, `border`, 20 px padding (16 under 640 px), `shadow-card`. The raised things on a page are approval and review cards, inbox rows, `OptionList`, group cards (`.list-card`), documents (a Memory section, project notes, a skill's instructions), Knowledge search results, tone cards and the composer, nothing else; never nested; nothing bordered inside it (CodeBlock and DiffView use tints) |
| 25 | `ListRow` | ad hoc rows, stat cards | `href?`, `title: string\|Snippet`, `description?`, `meta?: (string\|Snippet)[]`, `trailing?: Snippet`, `avatar?: string`, `selected?` | title `text-body` (500 when it links), description muted, meta `text-small muted tabular-nums` right-aligned, trailing control; hairline; whole row clickable when `href` (the link stretches over the row, `.row-stretch`, and the trailing control sits above it); meta folds under 640 px; titles truncate with `title` |
| 26 | `SettingsSection`, `SettingsRow` | `Section.svelte`, `ConfigField.svelte` | `title`, `bare = false`; `label`, `help?`, `for?`, `status?: 'saving'\|'saved'\|null`, `error?`, `children` | §11.1: the section's rows on one group card unless `bare`; a row says "Saving…", marks the page's "Changes saved" when it saves, and says "Saved" for 2 s only while no header shows it (`ui/saved.svelte.ts`) |
| 27 | `AdvancedField` | `ConfigField` + `SourceChip` | `path`, `label`, `type`, `options?` | SettingsRow + "Changed · Reset to default" or "Set by the server environment ({NAME}). Change it there." (read-only) |
| 28 | `Count`, `Dot` | chip counts, bell badge | `n`; `label` | Count: muted `tabular-nums`, hidden at 0, no background. Dot: `size: 6 \| 8 = 8`, ember `bg-warning rounded-full` + visually hidden label; 6 px only beside the Approvals count (D23) |
| 29 | `Avatar` | footer avatar | `name`, `size: 24\|32 = 24` | `rounded-full bg-hover text-muted` (subtle would vanish on the canvas), initials `text-caption`/`text-small` 600; no colours, no images; `aria-hidden` |
| 30 | `CodeBlock`, `CommandBlock` | `.mono` blocks | `code`, `wrap = false`, `copy = true`, `maxLines?`, `lineNumbers?`, `label?` | `bg-subtle rounded-md p-3 text-code`, no border; Copy icon top right (visible on hover/focus, always on coarse), "Copied" for 1.5 s; `wrap` uses `pre-wrap` + `overflow-wrap: anywhere`; "Show all {n} lines"; content from outside passes through `Verbatim`. `CommandBlock` = single line, `wrap` on. |
| 31 | `DiffView` | `DiffView.svelte` | `diff`, `mode: 'code'\|'new-file'\|'deleted-file'\|'prose'`, `path?`, `maxLines = 200` | code: one line-number gutter, `+` in `accent-text` and `−` in `danger-text`, added lines `bg-diff-add`, removed `bg-danger-subtle`, text stays `text`; hunk headers become "Lines 10–24" separators (`text-caption muted`); `---`/`+++` become one header row (path + "+24 −6"); deleted-file: "The whole file will be removed ({n} lines)." + plain preview under "Show content"; prose: inline insertions on `bg-diff-add`, deletions struck through on `bg-danger-subtle`; glyphs carry meaning, colour never alone |
| 32 | `FileToken` | attachment chips | `name`, `size?`, `state?: 'uploading'\|'error'`, `onremove?`, `href?` | `inline-flex h-8 items-center gap-2 rounded-md bg-subtle px-3 text-small`; the only chip-like element, because it is a removable object |
| 33 | `Tooltip` | `title=` attributes | `text`, `children` | `bg-text text-bg rounded-sm px-2 py-1 text-small`; 400 ms delay on hover, immediate on focus, Esc dismisses; fine pointers only; never the only place information lives |
| 34 | `Spinner` | ad hoc | `size: 12\|16` | 1.5 px arc, 800 ms linear; static "…" under reduced motion; paired with text for screen readers |
| 35 | `ProgressBar` | spend bars | `value`, `max`, `tone?`, `valueText` | 4 px `rounded-full`, track `bg-border`, fill `bg-accent` / `bg-warning-text` / `bg-danger`; `role="progressbar"` with `aria-valuetext` in money |
| 36 | `EmptyState` | `Empty.svelte` | `text`, `more?`, `picture?` (Bothy's logo beside the text; Approvals → Waiting only), `action?` | §13.1 |
| 37 | `Page`, `PageHeader` | `.page`, page headers | `title`, `back?: {href, label}`, `actions?: Snippet`, `tabs?`, `saves?` (the "Changes saved" line; on by default under `/settings/`) | §6.2 |
| 38 | `Kbd` | `.kbd` | `keys` | shortcuts dialog only |
| 39 | `Sidebar`, `ChatList`, `TopBar`, `AccountMenu`, `BannerHost`, `SearchDialog`, `ShortcutsDialog`, `PauseDialog` | `Shell.svelte`, `chat/Sidebar.svelte` | — | §4.2–4.4, §6.3–6.4, §11.6. Mobile drawer: `role="dialog"`, `aria-modal`, focus trap. |
| 40 | `Composer`, `ModelPicker`, `RefineToggle` | `chat/Composer.svelte` | `running`, `paused`, `refine` (bindable), `model` (bindable), `models`, `estimate?: {usd?, multiplier?, drafts}`, `onsend(text, files, opts)`, `onstop` | §7.5; states: empty, typing, sending, running, running with text, paused, drag-over, uploading, upload failed |
| 41 | `ActivityLine`, `ActivityGroup` | `chat/ToolRow.svelte` | `step: ToolStep`; `steps: ToolStep[]`, `live` | §7.4; line is a `<button aria-expanded>`; live state changes to waiting and failed announced politely (1 s throttle) |
| 42 | `ApprovalCard`, `ApprovalVotes`, `DecidedLine` | `ApprovalCard.svelte` | `approval`, `mode: 'inline'\|'inbox'\|'decided'\|'readonly'`, `viewer`, `onresolve(choice, comment?)` | renders only from the server `card` (or `lib/cardcopy.ts` fallback); states: waiting for me, waiting for others (requester, already voted, not eligible), parked, submitting, decided, expired, cancelled, submit failed; A/S/D only in inbox mode or when the card has focus |
| 43 | `ProviderChooser` | `ProviderSetup.svelte`, `StepModels.svelte` | `mode: 'setup'\|'settings'`, `onconnected?` | §5.6; in settings mode lives in a 560 dialog with `framed=false` lists |
| 44 | `BarChart` | `BarChart.svelte` | `data: {label, value, title}[]`, `format`, `height = 160` | §10.5 |
| 45 | `Verbatim`, `Markdown` | kept | unchanged APIs | restyled to tokens; Markdown gets the heading map (§3.2). `QRCode` and `Pairing` went with linking. |
| 46 | `Loading` | per-screen loading lines | `failed?`, `retry?`, `text?` ("This page didn't load. Check your connection and try again." unless it names a dialog's list or a search) | §13.2: "Loading…" after 400 ms; failed: `role="alert"` EmptyState with secondary "Try again". `ScreenLoading` wraps it for a chunk still on its way. |
| 47 | `Icon` | inline `<path>` icons | `name: IconName`, `size = 16`, `class?` | `<svg><use href="#i-{name}"></svg>` from the `index.html` sprite (§3.7); the stroke settings sit on the outer `<svg>` and inherit; `aria-hidden`, `focusable="false"`. The only component that may write `<svg>` |

---

## 16. Accessibility requirements

These are acceptance criteria, not guidance.

**Contrast and colour**
- Every text pair in §3.1 meets the listed ratio (4.5:1 for text, 3:1 for control boundaries, focus rings, chart bars and standalone dots); `tokens.test.ts` computes each pair from `app.css` in both themes. axe in `a11y.test.ts` and the e2e smoke passes on every route in both themes.
- Nothing is conveyed by colour alone: statuses are words; glyphs pair with text; diff lines carry `+`/`−`; links are underlined; the attention and waiting dots have hidden text.

**Keyboard**
- Every action is reachable and operable by keyboard; tab order follows reading order; no keyboard traps outside open dialogs and drawers.
- Approval flow without a pointer: sidebar → Approvals → J/K → Enter → A/S/D; focus lands on the next waiting item; the result is announced. A held key decides one item.
- Shortcuts are single keys only inside the inbox or a focused card, never while typing in a field. "Always allow" has no shortcut.
- A "Skip to content" link is the first focusable element.
- A sidebar family's toggle (§4.2) is a real button with `aria-expanded` and `aria-controls`: Enter and Space open and close it, focus stays on it, and the tab order is title, toggle, ⋯. The related chats are a nested, labelled list, not a `tree`.

**Focus**
- Visible focus on every focusable element (§3.5), never removed. Rows draw focus inside; cards never get a second outline.
- Dialogs and drawers trap focus, focus the first field (Cancel in destructive dialogs), close on Esc, and return focus to the trigger.
- After route changes focus moves to the page `<h1>` (programmatic, `tabindex="-1"`), except in a chat, where it moves to the composer.

**Semantics**
- Landmarks: `<nav aria-label="Main">` for the sidebar, `<main>` for content, `<header>` not used at app level. One `<h1>` per page (the page title; in chat, the chat title), sections as `<h2>`.
- Real `<button>` and `<a>` elements; `<label for>` on every field; errors linked with `aria-describedby` and `aria-invalid`.
- Disabled-with-a-reason controls use `aria-disabled="true"` (still focusable) and point to the visible reason.
- Component roles as in §15.3: `switch`, `radiogroup`, `menu`/`menuitem`/`menuitemradio`/`menuitemcheckbox`, `listbox`, `combobox`, `progressbar`, `dialog` with `aria-modal`. Route tabs are links with `aria-current="page"`, not a `tablist`.
- Icon-only buttons have `aria-label` and a matching tooltip.

**Live regions**
- One polite region in the shell for decisions, saves ("Saved"), copies ("Copied") and connection state. Toasts use `role="status"`.
- Activity lines announce only changes to waiting and failed (throttled to 1 s). Streaming text is not announced token by token; when a reply finishes, the region says "Bothy replied." Errors that follow a person's action use `role="alert"` once.

**Touch, zoom and motion**
- Targets are at least 44 × 44 px on `pointer: coarse` and 24 × 24 px elsewhere (WCAG 2.5.8).
- No horizontal page scroll at 360 px or at 320 CSS px (400 % zoom of 1280); text resizes to 200 % without loss.
- Inputs use 16 px text on coarse pointers and under 640 px.
- `prefers-reduced-motion` removes transforms and the spinner animation.
- No keyboard hints, hover-only actions or tooltips-as-content on touch.

**Content**
- Plain language per §14; times include an accessible full date in `title` or `aria-label` where relative ("2 h ago" → "14 October 2026, 10:42").
- Charts have a text summary and a visually hidden data table.
- `Verbatim` keeps making bidi and zero-width characters visible in identifiers.

---

## 17. Server work the UI needs

The UI never parses error strings or invents sentences from model output where structured data can exist. Each need has a fallback so no UI chunk blocks on the server. Tracks SV1–SV4 are Go work, separate from the web chunks; they own regenerating `web/src/lib/api/types.gen.ts`.

| # | Need | Used by | Fallback until it lands | Track |
|---|---|---|---|---|
| S1 | `GET /api/setup/state` returns `install: compose \| docker \| binary \| unknown`; `bothy setup-token` prints the full setup link when the public URL is known | §5.3 | Show the compose command, list the others in the disclosure | SV2 |
| S2 | Plain card copy in `core.Card`, rendered by the policy module from the Action only: `question`, `consequence`, plain field labels, plain `warnings`, `primary_label`, `ing` phrase, `kind` (`action\|pull_request\|deploy\|review`), `tone` (`default\|destructive`) | §8, chat-app cards | `lib/cardcopy.ts` builds the same strings client-side (web only) | SV1 |
| S3 | Bedrock detection in `POST /api/providers/detect`: `{kind: bedrock, region, source}` where `source` is human text ("instance role bothy-prod", "SSO profile acme-dev", "IRSA role"), never a value; the probe lists usable Claude and Nova models | §5.6 | No "Found on this server" AWS row; Bedrock in the provider list with "Check access" | SV2 |
| S4 | Multi-approver data on `Approval`: `approvals {required, votes [{user, name, choice, at, comment}], eligible [{user, name}], any_admin}`, `requester {user, name}`, vote events | §8.5 | Single-approver card without the Approvals row | SV1 |
| S5 | Friendly model data in the probe and `/api/models`: `display_name`, `short_name`, `provider_name`, `recommended`, `quick_tasks_default` | Setup, pickers, Settings, Spending | Show the model id with the provider prefix stripped | SV2 |
| S6 | One-call connect for setup: store key, write provider (with `allow_private` for a detected or explicitly allowed address) and set both model slots atomically | §5.6.5 | The client runs the existing vault, config and slot calls in sequence on Continue | SV2 |
| S7 | `display_name` on owner creation; `POST /api/setup/complete` callable on entering Step 3 | §5.5, §5.7 | Username as the name; complete on "Start chatting" | SV2 |
| S8 | Names next to ids everywhere: usage rows, audit rows, approvals (`decided_by_name`), runs (`requested_by_name`, `chat_title`, `schedule_name`) | Activity, cards | Username; "a chat" | SV3 |
| S9 | Admin-created users get a generated one-time password and `must_change_password`; sign-in returns the flag | §11.5, §5.9 | Admin types a password in the Add person dialog; no forced change | SV2 |
| S10 | Refine estimate: `GET /api/sessions/{id}/refine-estimate → {usd, multiplier, drafts}` from recent answer-step cost × the configured work, early stop considered | §7.5 | Multiplier from config only; else the generic sentence | SV3 |
| S11 | Transcript tool parts carry `state: running \| waiting_approval \| done \| failed \| denied \| expired \| blocked` | §7.4 | Client derives state from tool, approval and result events | SV3 |
| S12 | Inbox item types: MCP tool reviews, changed skills, unknown senders, new group chats, note suggestions exposed as approvals-inbox items | §8.8 | Keep them on their settings pages; drop the banners | SV1 |
| S13 | `code` on provider, channel and GitHub test failures: `key_rejected`, `no_model_access`, `out_of_credit`, `rate_limited`, `provider_error`, `timeout`, `unreachable`, `private_address`, `no_models`, `tls`, `not_a_model_server`, `aws_no_credentials`, `aws_denied`, `aws_no_models`, `unknown`, plus the raw message | §5.6.4, §12 | Client maps HTTP status and known substrings; unknown otherwise | SV2 |
| S14 | No session is persisted before its first message; the title is generated after the first reply; search returns the same title | §4.2, §7.2 | Client hides sessions with no messages and uses the first 48 characters | SV3 |
| S15 | `/api/search` returns plain-text snippets with match ranges, grouped chats / notes / code | §6.3 | Client strips Markdown and highlights terms | SV3 |
| S16 | Audit rows carry a one-line `summary`; egress rows one merged `outcome` (`reached`, `allowed_unreachable`, `blocked`) | §10.6 | Client builds summaries from action names and merges decision + result | SV3 |
| S17 | Deploy events `release.dispatched` and `release.finished {ok, url, duration, failed_step}` | §8.7, §10.4 | Poll `release_status` every 15 s while a deploy is open | SV1 |
| S18 | Knowledge HTTP API (F6): repositories with index status and progress; project notes get / put (`If-Match`) / history; search over code and notes with citations | §9 | Mock only; Knowledge shows the "Connect GitHub" state in production until it lands | SV4 |
| S19 | GitHub connect API (F4, F5): test a token or app → account + accessible repositories; repository allowlist; list a repository's workflows; environments read and write through config | §12.2 | Workflow as a text field | SV4 |
| S20 | Default persona text in company voice (the default SOUL.md no longer says "personal assistant running on your owner's own machine"); default tone "Warm and brief" | §11.3 | — (one embedded file) | SV2 |
| S21 | Self-service profile `GET`/`PATCH /api/me`: display name and time zone (the preferred chat app and where approval requests reach someone went with linking; the server refuses them) | §11.2 | Admins edit through `/api/users`; members see their name read-only | SV5 |
| S22 | `agent.name` and `agent.tone` in config (validated, hot) | §11.3 | General rows hidden | SV5 |
| S23 | `last_login_at` on users | §11.5 | "Joined {date}" | SV5 |
| S24 | Plain credential `source` on `/api/providers` rows | §11.4 | No "Credentials from" line | SV5 |
| S25 | Who paused and when: `paused_by`, `paused_by_name`, `paused_at` on Me and in the paused banner | §4.9, §11.6 | "Bothy is paused." without a name | SV5 |
| S26 | Knowledge notes history, indexing progress, `stale`, one-line `summary` per project | §9 | No history; indeterminate progress; first sentence built client-side | SV5 |
| S27 | Skill file endpoint and the changed-skill diff on inbox items | §8.8 | "Open the skill" link only | SV5 |
| S28 | `POST /api/search/test` for web search settings | §12.5 | Save, then wait for the first search | SV5 |
| S29 | Task rows with `origin`, a plain `reason` when stopped or failed, and `refined` | §10.1, §10.2 | Origin from the chat list; "Stopped" alone | SV5 |
| S30 | Deploy rows with requester and approver names, short sha and commit subject (untrusted: quoted, inert, never in card questions) | §10.4 | Usernames; ref only | SV5 |
| S31 | `GET /api/auth/methods` and the provider round trip: `POST /api/auth/oauth/{p}/start` → provider → `GET /api/auth/oauth/{p}/callback`, which lands on `next` or `/login?signin_error=…&provider=…` (a refused account in the refusal cookie) | §5.9 | — (landed with SPEC §15) | — |
| S32 | `GET /api/me/sign-ins`, `POST` and `DELETE /api/me/sign-ins/{p}` | §11.2 | — (landed with SPEC §15) | — |
| S33 | `GET`/`PATCH /api/sign-in`, `PATCH /api/sign-in/{p}`, `POST /api/sign-in/{p}/check` | §11.6 | — (landed with SPEC §15) | — |
| S34 | Bothy's picture: `GET /api/agent/picture` (`?download=1` names the file after Bothy), `GET /api/agent/picture/info` (`custom`, `version`), `PUT` (an image, ≤ 5 MB) and `DELETE` (back to the default) answering `PictureChange` with one plain `detail` per chat app (`updated`, `failed`, `manual` for Slack); Slack's `app_id` on `ChannelStatus` for the link to its app settings | §11.3, §12.3 | — (landed with SPEC §8.3) | — |
| S35 | Related chats: `parent_id`, `root_id` and `schedule_id` on sessions, `parents` on `GET /api/sessions`, `from` on the session detail, a fork listed at once under its chat | §4.2, §7.1 | — (landed with SPEC §14.10) | — |
| S36 | Starters: `GET /api/starters`, up to four rows for the viewer; schedule examples and check-in lines (`GET /api/schedules/examples`) | §7.2, §10.3 | — (landed with SPEC §14.10) | — |
| S37 | The welcome chat: `Me.welcome_pending`, `POST /api/me/welcome` | §5.9, §7.3 | — (landed with SPEC §14.10) | — |
| S38 | Quiet check-ins: a check-in's chat stays hidden until it parks, finds something or someone writes in it; schedule results titled "{name} · 19 Oct", "Check-in · 19 Oct" ("{name} · 19 Oct, 14:05" for a later one the same day); a clean chat of a result is "{name} (clean)" | §4.2, §7.3 | — (landed with SPEC §14.10) | — |

S21–S30 landed together (SPEC §14.7, "UI state"); their fallbacks are gone from the UI.

Team access (SPEC §5.1, §5.2, §5.9) replaced linking: `GET /api/teammates`, `PATCH /api/identities/{channel}/{external}` (`status` teammate or blocked, `can_approve`), `DELETE /api/teammates/…`, `GET`/`PATCH`/`DELETE /api/spaces` (`SpaceView`, admins), `GET /api/team/schedules`, `GrantView` on `GET /api/grants`, `kind` and `app` on usage rows, `approver.team` and the chat requester on approvals (with `kind` and `scheduled`), `member_check` and `idle_servers` on Discord's `ChannelStatus`, `chat_spent_micros` and `chat_limit_micros` on `/api/usage/today`, banner events `new_channel`, `team_access` and `chat_budget_80` (the last two shown on their pages, not as banners). The pairing routes and events are gone.

---

## 18. Implementation checklist

### 18.1 Shape of the work

```
F1 Foundations ──► F2 Primitives ──► ┌ C1 Shell & navigation            ┐
                                     │ C2 Setup & sign-in               │
                                     │ C3 Chat                          │
                                     │ C4 Approvals                     │
                                     │ C5 Knowledge                     │ ──► C10 Cleanup & enforcement
                                     │ C6 Activity                      │
                                     │ C7 Settings: account, general,   │
                                     │    models, people                │
                                     │ C8 Settings: connections         │
                                     └ C9 Settings: security, advanced  ┘
Server tracks SV1–SV4 run alongside; every UI chunk ships with its fallbacks.
```

- **F1 and F2 are serial** (F2 starts as soon as F1's `app.css` merges). **C1–C9 run in parallel** and touch disjoint files. **C10** runs last.
- **Ownership rule:** a chunk edits only the files it owns (listed below), plus new files under its own directories. It may import anything. If it needs a change in someone else's file, it asks that owner, or adds a local helper and flags it for promotion in C10.
- **Migration rule:** until C10, v0.1 screens keep working: F1 adds the new tokens with legacy aliases, maps new routes to v0.1 screens where they exist, and every replaced screen is deleted by the chunk that replaces it.
- Suggested order with fewer engineers: C1, C2, C3, C4, then C7, C9, C6, C8, C5 (C5 also waits on S18).

### 18.2 Gates that apply to every chunk

A chunk merges only when all of these pass:

| Gate | Check |
|---|---|
| G1 Build | `cd web && npm run check && npm test && npm run build`, then `scripts/budgets/ui-size.sh`: **first screen (the scripts `index.html` loads, their static imports and its stylesheets) JS ≤ 120 KB gz and CSS ≤ 20 KB gz; all JS chunks together ≤ 290 KB gz** (260 KB until v0.2 sign-in and GitHub App creation added lazy settings code; 280 KB until the Friendly look the owner chose (D46–D52): moving every icon into the `index.html` sprite saved about 560 B, but the tone cards, spending summary, chat picture and breadcrumb, nav and type icons and the saved line came to 287,925 B against 284,558 B, over by 1,205 B; the plan's fallback cuts (a text-only spending line, shorter samples) were worth about 380 B, and even dropping the tone cards (760 B, measured) would not have fit; the first screen is unchanged at ≤ 120 KB), no inline `<script>`, no `<style>` or `style=` in the built `index.html`, no other origins. Chat, the shell and their primitives are the entry; setup and sign in, the approvals inbox, Knowledge, Activity and Settings are lazy route chunks, prefetched on idle and on hover or focus. The PR states the first-screen and total gzip delta. |
| G2 Smoke | `npm run smoke` (production build, mock gateway, gateway CSP): no CSP violations, page errors or external requests; axe passes on the chunk's routes in light and dark. |
| G3 Screenshots | The chunk's shot spec (`web/e2e/shots/<chunk>.spec.ts`) captures **every route and state the chunk owns at 1280 × 800 and 375 × 812, in light and dark** (4 images per state, plus `-full` when the page overflows; full-page captures hide the inset panel's fixed frame, `[data-frame]`). A second engineer reviews them against this document; the PR links the set. |
| G4 Layout probes | Run by the shot helper on every capture: no sideways scroll at the capture's width, and again at 360 px after each phone capture; the first visible `[data-edge]` (the chat header) or else the `h1` starts at x = 408 ± 1 at 1280 px on column pages and chat; no rendered text under 12 px; no `backdrop-filter`; every `box-shadow` outside floating layers and focus rings is one of the named elevations (§3.4), compared by computed value; no bordered element inside another bordered element except overlays. |
| G5 Source greps (`web/scripts/ui-lint.sh`, part of `npm run check`) | None of: `chip`, `backdrop-blur`, `rounded-(2xl\|3xl)`, `rounded-xl` outside `setup/Frame.svelte`, bare `rounded` class, `shadow-(xs\|sm\|md\|lg\|xl\|2xl)`, literal `shadow-[…]`, `text-warning` without a suffix, `fill-*` utilities, `<svg` outside `Icon.svelte` and `lib/icons.ts`, `animate-(pulse\|ping\|bounce)`, `@font-face`, `text-display`, `uppercase`, `tracking-wide`, `font-bold`, arbitrary `text-[…px]`, `style="`, legacy colour names (`fg`, `sunken`, `line`, `ok`, `info`, `risk-`, `r1`–`r6`), Tailwind palette colours (`(bg\|text\|border)-(red\|green\|blue\|amber\|yellow\|emerald\|slate\|gray\|zinc\|white\|black)`). |
| G6 Copy | `assertPlainCopy(page)` passes on the chunk's main-flow routes (Details, Technical details, Raw record and Advanced excluded): no match for the two patterns below; no `!` in UI strings; no `usr_`/`ses_`/`run_`/`apr_`/`sch_` ids; no "Untitled chat" or "never" as a time. Strings match this document exactly where it gives them. |
| G7 Tests | Unit tests for new logic; existing tests move with their components and keep passing. |
| G8 Accessibility | The chunk's §16 items hold: keyboard path for every action, focus return on overlays, labels and `aria-describedby` on fields, live-region messages, 44 px targets under `pointer: coarse` (Playwright with `hasTouch` and a coarse-pointer emulation). |
| G9 Scope | Only owned files changed (reviewer checks the diff against the list). |

G6 patterns (shared by `web/e2e/lib/copy.ts` and, in C10, `web/src/copy.test.ts`):

```
/\b(context window|TTFT|cron|sandbox|egress|principal|use_tool|tool_search|trifecta|micro-?dollars?|heartbeat|session|vault|SOUL\.md|USER\.md|MEMORY\.md|HEARTBEAT\.md|household)\b/i
/\btokens?\s+(in|out|used|of)\b|\b\d[\d.,]*K?\s+tokens\b/i
```

### 18.3 Chunks

#### F1 Foundations (first, one engineer)

Owns: `web/src/app.css`, `web/src/legacy.css` (new, temporary), `web/src/tokens.test.ts` (new), `web/index.html`, `web/public/**`, `web/src/lib/icons.ts`, `web/src/components/Icon.svelte`, `web/src/lib/format.ts` + test, `web/src/App.svelte` and `web/src/lib/routes.ts` + test (until C1 takes them), `web/src/components/approval/index.ts` (until C4 takes it), `web/mock/server.ts`, `web/mock/fixtures.ts`, `web/mock/domains/**` (new), `web/e2e/lib/**` (new), `web/e2e/ui.spec.ts`, `web/.gitignore`.

- [ ] Replace the theme with §3.8 **without** the four `initial` resets. Add legacy aliases in `@theme` (`--color-fg: var(--color-text)`, `--color-sunken: var(--color-subtle)`, `--color-line: var(--color-border)`, `--color-line-strong: var(--color-control)`, `--color-ok: var(--color-accent-text)`, `--color-warn: var(--color-warning-text)`, `--color-bad: var(--color-danger-text)`, `--color-info: var(--color-accent-text)`, `-soft` variants, `risk-*` → `muted`, `r1`–`r6` → `chart`). Move v0.1 component classes (`.chip*`, `.stat`, `.alert-*`, old `.btn*`) into `legacy.css`, rewritten on the new tokens, with blur and `shadow-xs` removed.
- [ ] `tokens.test.ts`: parse `app.css` and assert every pair and ratio in §3.1 for both themes.
- [ ] Add the new icons (`clock`, `spinner`, `back`, `external`, `copy`, `refresh`, `arrow-up`, `stop`); keep old ones until C10.
- [ ] `format.ts`: `money()`, `estimate()`, `relTime()`, `fullTime()`, `duration()`, `expiresIn()`, `plural()` per §14.2, with tests (including "<$0.01", "Free", "$1,240", "Expires in less than a minute").
- [ ] Routes: the final pattern list and redirects (§4.5) with a test per redirect row. New routes point at the v0.1 screen that covers them where one exists (`/activity/spending` → `Usage.svelte`), otherwise at a stub file at the final path owned by the later chunk (for example `screens/knowledge/Knowledge.svelte`). Register `/dev/ui` → `components/ui/Gallery.svelte` behind `import.meta.env.DEV`.
- [ ] `components/approval/index.ts` re-exports the v0.1 `ApprovalCard` so C3 can import the final path before C4 lands.
- [ ] Mock: split `fixtures.ts` and the route handlers of `server.ts` into `web/mock/domains/{shell,setup,chat,approvals,knowledge,activity,settings,connections,security}.ts`, registered from `server.ts`, so each chunk edits only its domain file. Add scenario switches (query `?mock=`) for states that need them (banners, failures, detection results).
- [ ] `web/e2e/lib/shots.ts` (capture routes × {1280×800, 360×780} × {light, dark}, `-full` option, the G4 probes) and `web/e2e/lib/copy.ts` (`assertPlainCopy`). Output to `web/e2e/shots/out/` (gitignored). Capture a baseline of v0.1 under the new tokens.
- [ ] `index.html` metas (`theme-color`, description) and the new favicon, maskable and touch icons (§3.7).

Acceptance: G1, G2, G7. v0.1 screens render on the new palette in both themes; dark primary buttons are deep green with white text; `tokens.test.ts` passes; every old route resolves or redirects; baseline shots exist.

#### F2 Primitives (one or two engineers, after F1's `app.css`)

Owns: `web/src/components/ui/**` (all primitives in §15.3 rows 1–38, `toast.svelte.ts`, `live.svelte.ts` with `announce()`, `Gallery.svelte`, tests).

- [ ] Build every primitive with the props, states and ARIA in §15.3 and the recipes in §15.2.
- [ ] `Gallery.svelte` shows each primitive in every state for review.
- [ ] Unit tests for behaviour: Dialog (focus first / Cancel, Esc, return focus, no close while busy, `dirty`), Drawer (trap, Esc), Menu (arrows, typeahead, Esc), Listbox and Combobox (`aria-activedescendant`, filtering, multi), OptionList (arrow keys, expanded slot), Switch (`role="switch"`), Disclosure (`aria-expanded`), Button (`aria-disabled` + reason, loading width lock), KeyField (paste triggers, debounce, abort), Toast (one at a time, `role="status"`), DiffView (hunk header → "Lines a–b", deleted-file mode).

Acceptance: G1, G5, G7, G8 on the gallery; gallery shots at 1280 and 360 in both themes reviewed; axe clean on `/dev/ui`; the gallery is absent from the production bundle.

#### C1 Shell and navigation

Owns: `web/src/App.svelte`, `web/src/App.test.ts`, `web/src/session.test.ts`, `web/src/lib/routes.ts` + test, `web/src/lib/router.svelte.ts` + test, `web/src/lib/stores/ui.svelte.ts`, `web/src/lib/stores/session.svelte.ts`, `web/src/components/shell/**`; deletes `web/src/components/Shell.svelte`, `web/src/components/Toasts.svelte`, `web/src/screens/chat/Sidebar.svelte`; `web/mock/domains/shell.ts`; `web/e2e/shell.spec.ts`, `web/e2e/shots/shell.spec.ts`.

- [ ] Sidebar §4.2: wordmark, New chat, Search, nav with icons and the Approvals count, chat list grouped (Today, Previous 7 days, Older), running spinner, waiting dot, hover `⋯` (Rename, Delete chat…), Show more, footer Settings (admins) with attention dot, account row and menu.
- [ ] Settings mode §4.3 (admin and member variants); phone `/settings` list.
- [ ] Phone and tablet top bar and drawer §4.4, "{n} waiting" button.
- [ ] Banner store and host §4.9 with priorities and roles; attention dot store §4.10 (fed by security findings and connection status; hidden if unavailable).
- [ ] Search dialog §6.3, Keyboard shortcuts dialog §6.4, `PauseDialog` (§11.6 copy) used by the account menu and Security.
- [ ] Mount the toast host and live region (F2), skip link, focus to `h1` on route change, vault-sealed blocking screen (mounts C9's `UnlockScreen` for admins).
- [ ] Remove the top bar, spend meter, sandbox chip, Stop all and the persistent banner.
- [ ] Not found and no-access states (§13.1).

Acceptance: gates G1–G9. Shots: sidebar (plain, running and waiting chats, long list with Show more), settings mode admin and member, phone top bar with and without waiting items, drawer open, account menu, theme submenu, each banner (via `?mock=`), search empty / results / no results, shortcuts dialog, not found, no access. E2E: ⌘K, "/", "?" and Esc work; every row of the redirect table lands on its new route; sessions without messages never appear; members see no Settings item.

#### C2 Setup and sign-in

Owns: `web/src/screens/setup/**` (rewritten: `Setup`, `SetupCode`, `RemoteSetup`, `StepAccount`, `StepModel`, `StepReady` + tests; deletes `StepSafety`, `StepApps`, `StepYours`, `StepModels`), `web/src/screens/Login.svelte`, `web/src/screens/ChangePassword.svelte` (new), `web/src/components/ProviderChooser.svelte` + test, deletes `web/src/components/ProviderSetup.svelte`, `web/src/lib/providers.ts`, `web/src/lib/providerErrors.ts` (new), `web/src/lib/password.ts`; `web/mock/domains/setup.ts`; `web/e2e/setup.spec.ts`, `web/e2e/shots/setup.spec.ts`.

- [ ] Flow, frame and every screen in §5 with its exact copy.
- [ ] `ProviderChooser` with detected rows, provider list, More providers, KeyField checks, region select, model "Change", model notes, every failure in §5.6.4 (`providerErrors.ts` maps S13 codes, with the HTTP-status fallback), Allow-address action.
- [ ] Continue with the visible reason, single save on Continue (S6 fallback sequence), save failure.
- [ ] Silent `mode: multi` and time zone; display name (S7 fallback).
- [ ] Sign in states and the first-password screen (S9 fallback: route unused until the flag exists).

Acceptance: gates G1–G9. Shots for every state: setup code (empty, each error), remote screen; Step 1 (empty, each field error, password help states, network error); Step 2 (detecting, nothing detected, AWS detected checking / connected / region changed / each AWS failure, Ollama connected / small-model notes / no models, key provider checking / connected / Change open / each failure, Other server with private-address Allow, More providers open, Continue reason, saving, save failed); Step 3; sign in and its errors; choose a new password. E2E: with a token link and mocked AWS detection, `/chat` is reached in 3 screens, 3 typed values and 3 presses; no vault or config write happens before Continue; the whole setup works keyboard-only.

#### C3 Chat

Owns: `web/src/screens/chat/{Chat.svelte,ConversationView.svelte,Composer.svelte,conversation.svelte.ts}` + tests (`ConversationView.test.ts`, `streaming.test.ts`), deletes `web/src/screens/chat/ToolRow.svelte` + test, `web/src/components/chat/**` (new), `web/src/lib/activity.ts` + test (new), `web/src/lib/transcript.ts` + test, `web/src/components/Markdown.svelte`, `web/src/lib/markdown.ts` + test (heading levels only; security behaviour unchanged), deletes `web/src/components/StackedBar.svelte`; `web/mock/domains/chat.ts`; `web/e2e/chat.spec.ts`, `web/e2e/shots/chat.spec.ts`.

- [ ] Layout, header, chat menu, scroll and day separators (§7.1); empty chat and suggestions (§7.2); messages, actions, citations, Markdown styles (§7.3).
- [ ] Activity lines, folding, Details and the full phrase table with derived state (§7.4); `use_tool` hidden.
- [ ] Composer with commands, attachments, Refine and cost line, model picker, Stop, paused (§7.5).
- [ ] Notices and errors (§7.6); Chat details, Pinned, Continue in a clean chat, Delete (§7.7).
- [ ] Imports `ApprovalCard` and `DecidedLine` from `components/approval` (C4's API in §15.3 row 42).

Acceptance: gates G1–G9. Shots: empty chat (with GitHub, without, admin without GitHub), populated chat, attachment message, "Working…", streaming, tool running, folded summary, expanded group with Details open, waiting approval, each decided line, failed step, every notice in §7.6, single provider error, Refine on (estimate, multiplier only, neither), model picker open, slash menu, drag overlay, upload error, paused composer, chat menu, Chat details with Technical details open, Pinned, both dialogs, phone layouts. Unit: for a pending `fs_delete`, the line reads "Waiting for approval to delete …" and never "Deleted" until the result event; the summary rule; every tool in §7.4 has all phrases. E2E: exactly one error line and no ".)."; no "Working"/"Thinking" after the first text delta; a decided card is still in the transcript after reload; no session is stored before the first message; a suggestion fills but does not send; Enter inserts a newline under coarse-pointer emulation.

#### C4 Approvals

Owns: `web/src/components/approval/**` (`ApprovalCard`, `ApprovalFields`, `ApprovalVotes`, `DecidedLine`, `AlwaysAllowDialog`, `index.ts`) + tests (moved from `components/ApprovalCard.test.ts`), deletes `web/src/components/ApprovalCard.svelte`, `web/src/lib/cardcopy.ts` + test (new), `web/src/lib/labels.ts` (choice labels; other exports stay until C10), `web/src/lib/grants.ts` + test (adds `describeGrant`), `web/src/screens/Approvals.svelte` + test, `web/src/keys.test.ts`; `web/mock/domains/approvals.ts`; `web/e2e/approvals.spec.ts`, `web/e2e/shots/approvals.spec.ts`.

- [ ] Card anatomy, copy table, choices, warnings, decided lines, multi-approver viewer states, PR card, deploy follow-up (§8.1–8.7), from S2 data or `cardcopy.ts`.
- [ ] Inbox: tabs, sections, expand in place, item types, Done tab, keyboard and live region (§8.8).

Acceptance: gates G1–G9. Shots: a card for every row of §8.2, warnings, deny reason field, submitting, submit failed, Always allow dialog, every decided line, multi-approver (four viewer states, approved, denied), PR card with Show open, three deploy follow-up lines, inbox (For you plus Waiting for others), Done tab, each item type (someone waiting to be let in, a new Slack channel with the choice, a new Discord channel: `item-unknown-sender`, `item-let-in`, `item-channel-choice`, `item-channel-discord`), requests from chat apps (`approval-team-card`, `approval-team-channel`), empty states, phones (stacked buttons). E2E: J/K/Enter/A/S/D; Let in, and Turn on with the choice, from the keyboard; a held key decides one item; focus moves to the next item and the live region reads "Approved. Next: …"; the requester sees no decision buttons; deploy fixtures never show "Allow for this chat"; Deny never has a danger class; the danger fill appears only on irreversible and financial actions other than deploys.

#### C5 Knowledge

Owns: `web/src/screens/knowledge/**` (`Knowledge`, `Projects`, `ProjectNotes`, `NotesEditor`, `NotesHistoryDialog`, `SuggestionsDialog`, `KnowledgeSearch`, `Memory`, `Skills`, `SkillView` + tests), deletes `web/src/screens/Memory.svelte` + test and `web/src/screens/settings/Skills.svelte` + test (moved), `web/src/lib/api/knowledge.ts` (new; local types until S18 is in `types.gen.ts`); `web/mock/domains/knowledge.ts`; `web/e2e/knowledge.spec.ts`, `web/e2e/shots/knowledge.spec.ts`.

- [ ] Projects, project page, edit, conflict, history, suggestions (as approval cards), first read, size warnings (§9.1–9.2).
- [ ] Search with filters (§9.3); Memory (§9.4); Skills and skill page (§9.5); role differences (§4.11).

Acceptance: gates G1–G9. Shots: projects (up to date, reading, out of date, couldn't read), no GitHub (admin, member), nothing chosen, project notes, first read, edit mode, discard confirm, conflict, size warning, history, suggestions notice and dialog with the outside-content warning, Technical details dialog, search (all, code, notes, repository filter, no results), Memory (view, edit, near full, saved, empty, history), Skills (admin, member, changed, empty), skill page. Unit: `If-Match` conflict flow; Memory save message. Mono appears only in code results and paths.

#### C6 Activity

Owns: `web/src/screens/activity/**` (`Activity`, `Tasks`, `TaskDetail`, `Schedules`, `ScheduleForm`, `CheckIn`, `Releases`, `Spending`, `Audit` + tests), deletes `web/src/screens/{RunTrace,Schedules,ScheduleEdit,Usage,Audit}.svelte` and move their tests, `web/src/components/BarChart.svelte`, `web/src/lib/schedules.ts`, `web/src/lib/taskStatus.ts` (new); `web/mock/domains/activity.ts`; `web/e2e/activity.spec.ts`, `web/e2e/shots/activity.spec.ts`.

- [ ] Tasks with filters, outcome words and member view (§10.1); task detail with Technical details (§10.2).
- [ ] Schedules list with Check-in, form with "When" presets and Custom repeat rule, More options, history; Check-in form (§10.3).
- [ ] Releases (§10.4); Spending with meter states, chart and lists (§10.5); Audit log with both views, row expansion, log check and Allow-site dialog (§10.6).

Acceptance: gates G1–G9. Shots: tasks (admin, member, filters, empty, every outcome word), task detail (Technical details open, refined), schedules (with Check-in, failed last run, empty, In chat apps and its delete confirm: `schedules-team`, `schedules-team-delete`), schedule form (new, edit, Custom, errors, More options), Check-in form, releases (deploying, waiting, failed, denied, empty), spending (7 and 30 days, meter at 50, 80 and 100 %, member, price not set, empty, phone), audit (actions, website access, expanded row, log intact and broken, Allow-site dialog, filtered empty, phone). Unit: "When" ↔ cron round trip for every preset; money in lists; rendered rows contain names, never ids. Chart bars are focusable and the hidden table matches the data.

#### C7 Settings: Account, General, Models, People

Owns: `web/src/screens/settings/{Settings,Account,General,Personality,Models,People}.svelte` + tests, deletes `web/src/screens/settings/{Tokens,Persona,Budgets}.svelte` and `web/src/components/ChainEditor.svelte`; `web/mock/domains/settings.ts`; `web/e2e/settings.spec.ts`, `web/e2e/shots/settings.spec.ts`; `ChatPeople.svelte` (People → In chat apps). Imports `ProviderChooser` (C2).

- [ ] Row pattern and saving behaviour (§11.1) through `SettingsRow`; no `ConfigField`, no provenance.
- [ ] Account (§11.2), General and the personality editor (§11.3), Models with Change, Backup models, Add a provider, Manage, Refine (§11.4), People with Add person result and dialogs (§11.5).

Acceptance: gates G1–G9. Shots: Account (sign-in methods, create token and the one-time token view, change password), General (three connection states, the picture: an admin's, a change some chat apps refused, a refused file: `general-picture-custom`, `general-picture-changed-partly`, `general-picture-refused`; personality editor), Models (providers including an error row, Change dialog, Add a provider dialog, Manage for Bedrock and for a key provider, Backup models open, Refine on with How thorough), People (list, Add person and its result, spending limit dialog, owner-only empty; In chat apps with approvers, someone no longer in the team and a blocked person, its daily limit, Block and Delete their data dialogs, empty: `people-chat-apps`, `people-chat-limit`, `people-block-dialog`, `people-delete-dialog`, `people-chat-none`), phone settings list. Unit: save on blur and change, "Saved" announced, restart help shown on restart-only fields.

#### C8 Settings: Connections

Owns: `web/src/screens/settings/connections/**` (`Connections`, `ChatApp`, `ChatAppManage`, `GitHub`, `GitHubManage`, `RepoPicker`, `EnvironmentDialog`, `McpServer`, `AddMcpDialog`, `WebSearch` + tests), deletes `web/src/screens/settings/{Channels,Mcp}.svelte` + `Channels.test.ts` and `web/src/components/ChannelSetup.svelte` + test, `web/src/components/Pairing.svelte`, `web/src/components/QRCode.svelte`; `web/mock/domains/connections.ts`; `web/e2e/connections.spec.ts`, `web/e2e/shots/connections.spec.ts`.

- [ ] Overview (§12.1); GitHub connect, repository picker, manage view, deploy environments and dialog (§12.2); chat apps steps, failures and manage view (§12.3); MCP add and server page (§12.4); web search (§12.5).

Acceptance: gates G1–G9. Shots: overview with every status word; GitHub (token, app, Enterprise open, connected line, each failure, repository picker, saved with the reading line, manage view, environments empty and listed, environment dialog with 1 and 2 approvals); Slack, Telegram and Discord (steps, success with the try-it line, failures, manage view with who can use Bothy and group chat details, Slack's picture row, the Discord turn-off confirm: `slack-manage-who`, `slack-manage-picture`, `telegram-manage-who`, `discord-manage`, `discord-turn-off-confirm`); MCP (add dialog for both transports, code-off message, server page, review); web search (each option, check passed). Keyboard: OptionList and multi Combobox.

#### C9 Settings: Security and Advanced

Owns: `web/src/screens/settings/security/**` (`Security`, `Permissions`, `Websites`, `SavedKeys`, `UnlockDialog`, `UnlockScreen` + tests), `web/src/screens/settings/advanced/**` (`Advanced` and its section components + tests), `web/src/components/AdvancedField.svelte`, deletes `web/src/screens/settings/{Policy,Sandbox,Vault,Advanced}.svelte` and `web/src/components/{SandboxStatus,PresetPicker}.svelte`, `web/src/lib/config.svelte.ts` + test, `web/src/lib/yaml.ts`; `web/mock/domains/security.ts`; `web/e2e/security.spec.ts`, `web/e2e/shots/security.spec.ts`.

- [ ] Security sections, sub-pages, saved keys and unlock, Pause and paused state (§11.6).
- [ ] Advanced sections with `AdvancedField` provenance rules (§11.7).

Acceptance: gates G1–G9. Shots: Security (with findings, without), preset options, timeouts, running code on and off with "How to turn it on" open, Permissions, Websites with blocked recently, Saved keys (list, add, sealed row, unlock dialog), Pause dialog and paused state; Advanced (every section, a changed field with Reset, an env-set field). Unit: `AdvancedField` shows "Changed · Reset to default" only for non-default values and disables env-set fields.

#### C10 Cleanup and enforcement (last)

Owns: `web/src/app.css`, deletes `web/src/legacy.css`, `web/src/lib/icons.ts`, `web/src/lib/labels.ts`, orphaned components (`Section`, `Empty`, old `Dialog`, old `DiffView`, `ConfigField`, `SourceChip`), `web/src/copy.test.ts` (new), `web/scripts/ui-lint.sh` (new), `web/package.json` (wire `ui-lint` into `npm run check`), `web/e2e/ui.spec.ts`, `web/e2e/shots/all.spec.ts`, `docs/SPEC.md` §9 (point to this document and drop superseded text).

- [ ] Turn on the four `initial` resets; delete the legacy aliases and `legacy.css`; fix anything that stops compiling.
- [ ] Drop unused icons and `labels.ts` exports (`risk`, `riskBar`, `verdictChip`, `runStatus` classes, `originIcon`); delete orphans.
- [ ] `copy.test.ts`: scan `.svelte` and `lib/*.ts` string literals for §14.3 words, `!`, ids and banned phrases, failing with file and line. Allowlist only `screens/settings/advanced/**`, components named `*Details.svelte` or `TechnicalDetails`, and test files.
- [ ] `ui-lint.sh`: the G5 greps over all of `web/src`.
- [ ] Full screenshot run of every chunk's spec plus the final route list in `ui.spec.ts`; review and sign-off of the complete set against this document.
- [ ] Update `docs/SPEC.md` §9.

Acceptance: every gate over the whole app; `--color-*: initial` in place and the build passes; `ui-lint` and the copy test are green; the full screenshot set (every route and state, 1280 and 360, light and dark) is signed off; the left-edge probe passes on every top-level route; budgets pass with headroom (aim for JS ≤ 90 KB gz, the SPEC target).

### 18.4 Definition of done for the redesign

- [ ] Setup reaches the first chat in 3 screens and 3 typed values with a detected option, 4 with a pasted key.
- [ ] No global top bar; at most one banner, only for the conditions in §4.9.
- [ ] Every page and the chat share the left edge (x = 408 at 1280 px); no horizontal scroll at 360 px on any route, both themes.
- [ ] No pills, badges, icon tiles (the logo aside), blur, uppercase labels, cards inside cards or text under 12 px anywhere.
- [ ] Approval cards ask a plain question with a verb button; decided cards stay in the transcript; a two-approver deploy shows "1 of 2", names and who can still approve; the requester has no buttons.
- [ ] Activity lines never use the past tense before the result; `use_tool` never appears.
- [ ] Names, never ids, outside Details; money, never tokens, outside Details.
- [ ] Refine shows its cost before sending; Bedrock appears as "Use this server's AWS access" when detected; GitHub, repositories, PR cards, release approvals and Knowledge with project notes all have their screens.
- [ ] axe clean on every route in both themes; keyboard-only approval flow works end to end.
- [ ] `scripts/budgets/ui-size.sh` passes: first-screen JS ≤ 120 KB gz and CSS ≤ 20 KB gz, all JS chunks ≤ 290 KB gz, no inline scripts or styles, no other origins.

---

## 19. Deferred (not in v0.2)

- Invite links for new people (S9 uses one-time passwords instead).
- A collapsible desktop sidebar.
- Command-palette actions in ⌘K beyond search ("New schedule", "Pause Bothy").
- Per-person avatar colours or images.
- Embeddings-based Knowledge search (F6 keeps FTS5).
- "Sign out everywhere" (needs a server-side session revoke).
- Branching a chat from one of its messages (the parent and root columns already support it).
- Bothy introducing itself when an admin turns a group chat on.
- "Also delete the chats with its past results" when deleting a schedule.
- A "Set up in “{chat}”" link on the schedule page.
- "In “{parent}”" next to a related chat in search results.
