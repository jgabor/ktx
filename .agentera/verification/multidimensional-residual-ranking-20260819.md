# Multidimensional residual-delivery ranking — 2026-08-19

Source: `historical-aggregates-20260812-19.md`  
Source SHA-256: `2af75efe91d7754dd60fc41f8d6b6c7e264f2eddecbaec1b21eafe7688d55f17`

The source method is reproduced from the linked implementation identities: inclusive RFC3339 bounds, first artifact by timestamp then ID, per-call `(characters+3)/4` estimates, the existing closed reporting vocabulary for command/subcommand, current output-size bands, descending delivered estimate, and the first 100 groups as detailed. Only aggregate-safe dimensions and counts are retained.

## Outcome

Select one experiment candidate: `read` / command `—` / subcommand `—` / profile `default` / route `default_fallback` / disposition `anti_worsening_rejected` / raw-output band `1k-10k`. It is the largest raw-delivered subgroup in both windows: 4,216 calls and 3,562,440 raw=delivered estimated tokens in 72 hours; 6,030 calls and 5,379,955 raw=delivered estimated tokens in seven days. It is reproducible because every retained dimension defines it. This selection authorizes only a bounded experiment, not a route, transform, rollout, or shipped-default change.

All target-tool groups are retained below, including groups outside the public top-100 detail boundary. Thus no unlisted target row can change target eligibility. Full-report omitted groups remain reconciled in the summary and cannot invalidate the target ranking because every target group among them is explicit.

Continuity remains unknown. Runtime-cache evidence remains separate with the source report's actual capture scope. Defaults remain unchanged.

## Full-report reconciliation

| Window    | Groups (detailed/omitted) | Detailed calls / artifacts / raw / delivered | Omitted calls / artifacts / raw / delivered | Complete calls / artifacts / raw / delivered |
| --------- | ------------------------- | -------------------------------------------- | ------------------------------------------- | -------------------------------------------- |
| 72-hour   | 100 / 43                  | 24,977 / 6,866 / 37,452,525 / 25,567,903     | 169 / 20 / 64,477 / 24,531                  | 25,146 / 6,886 / 37,517,002 / 25,592,434     |
| seven-day | 100 / 72                  | 36,194 / 10,239 / 55,743,460 / 36,997,374    | 672 / 66 / 236,332 / 99,255                 | 36,866 / 10,305 / 55,979,792 / 37,096,629    |

## Target disposition reconciliation

These complete totals include detailed and omitted placements. Fields are calls / artifacts / raw estimate / delivered estimate.

| Window    | Tool | Disposition               | Complete                               |
| --------- | ---- | ------------------------- | -------------------------------------- |
| 72-hour   | bash | `anti_worsening_rejected` | 7,329 / 0 / 3,670,376 / 3,670,376      |
| 72-hour   | bash | `artifact_persisted`      | 1,828 / 1,828 / 7,794,143 / 3,663,065  |
| 72-hour   | bash | `intentional_passthrough` | 879 / 0 / 908,955 / 908,955            |
| 72-hour   | grep | `anti_worsening_rejected` | 2,102 / 0 / 662,313 / 662,313          |
| 72-hour   | grep | `artifact_persisted`      | 1,197 / 1,197 / 3,778,844 / 1,291,734  |
| 72-hour   | read | `anti_worsening_rejected` | 5,087 / 0 / 4,276,104 / 4,276,104      |
| 72-hour   | read | `artifact_persisted`      | 2,327 / 2,327 / 8,632,406 / 4,086,893  |
| seven-day | bash | `anti_worsening_rejected` | 9,845 / 0 / 4,792,147 / 4,792,147      |
| seven-day | bash | `artifact_persisted`      | 2,695 / 2,695 / 11,924,035 / 5,050,929 |
| seven-day | bash | `intentional_passthrough` | 1,349 / 0 / 1,334,046 / 1,334,046      |
| seven-day | grep | `anti_worsening_rejected` | 3,540 / 0 / 1,066,152 / 1,066,152      |
| seven-day | grep | `artifact_persisted`      | 1,883 / 1,883 / 5,810,061 / 2,037,583  |
| seven-day | read | `anti_worsening_rejected` | 7,289 / 0 / 6,279,747 / 6,279,747      |
| seven-day | read | `artifact_persisted`      | 3,454 / 3,454 / 13,150,382 / 6,192,581 |

## Complete target rows

`Placement` records whether the row is inside or outside the current public top-100 detail boundary. `—` is the closed vocabulary's absent value.

| Window    | Placement | Tool | Command | Subcommand | Profile     | Route            | Disposition               | Band     | Calls | Artifacts |        Raw | Delivered |
| --------- | --------- | ---- | ------- | ---------- | ----------- | ---------------- | ------------------------- | -------- | ----: | --------: | ---------: | --------: |
| 72-hour   | detailed  | read | —       | —          | default     | default_fallback | `anti_worsening_rejected` | 1k-10k   | 4,216 |         0 |  3,562,440 | 3,562,440 |
| 72-hour   | detailed  | read | —       | —          | default     | default_fallback | `artifact_persisted`      | 10k-100k | 1,339 |     1,339 |  6,707,959 | 2,608,993 |
| 72-hour   | detailed  | bash | —       | —          | default     | default_fallback | `artifact_persisted`      | 10k-100k |   704 |       704 |  4,735,826 | 2,456,998 |
| 72-hour   | detailed  | bash | —       | —          | default     | default_fallback | `anti_worsening_rejected` | 1k-10k   | 2,215 |         0 |  1,558,911 | 1,558,911 |
| 72-hour   | detailed  | read | —       | —          | default     | default_fallback | `artifact_persisted`      | 1k-10k   |   988 |       988 |  1,924,447 | 1,477,900 |
| 72-hour   | detailed  | grep | —       | —          | search      | matched          | `artifact_persisted`      | 10k-100k |   540 |       540 |  2,611,142 |   728,319 |
| 72-hour   | detailed  | bash | —       | —          | default     | default_fallback | `artifact_persisted`      | 1k-10k   |   652 |       652 |  1,015,946 |   670,503 |
| 72-hour   | detailed  | read | —       | —          | default     | default_fallback | `anti_worsening_rejected` | 10k-100k |   128 |         0 |    617,271 |   617,271 |
| 72-hour   | detailed  | bash | git     | status     | git-status  | matched          | `anti_worsening_rejected` | 10k-100k |    98 |         0 |    593,204 |   593,204 |
| 72-hour   | detailed  | grep | —       | —          | search      | matched          | `artifact_persisted`      | 1k-10k   |   655 |       655 |  1,109,364 |   559,726 |
| 72-hour   | detailed  | grep | —       | —          | search      | matched          | `anti_worsening_rejected` | 1k-10k   |   926 |         0 |    554,014 |   554,014 |
| 72-hour   | detailed  | bash | —       | —          | passthrough | matched          | `intentional_passthrough` | 10k-100k |    44 |         0 |    402,915 |   402,915 |
| 72-hour   | detailed  | bash | —       | —          | default     | default_fallback | `anti_worsening_rejected` | 10k-100k |    81 |         0 |    369,581 |   369,581 |
| 72-hour   | detailed  | bash | ls      | —          | ls          | matched          | `anti_worsening_rejected` | 10k-100k |    61 |         0 |    348,084 |   348,084 |
| 72-hour   | detailed  | bash | —       | —          | default     | default_fallback | `intentional_passthrough` | 10k-100k |    39 |         0 |    217,277 |   217,277 |
| 72-hour   | detailed  | bash | git     | —          | default     | default_fallback | `artifact_persisted`      | 10k-100k |    94 |        94 |    685,457 |   206,745 |
| 72-hour   | detailed  | bash | —       | —          | default     | default_fallback | `anti_worsening_rejected` | 1-1k     | 2,140 |         0 |    173,427 |   173,427 |
| 72-hour   | detailed  | bash | git     | status     | git-status  | matched          | `anti_worsening_rejected` | 1k-10k   |   246 |         0 |    173,298 |   173,298 |
| 72-hour   | detailed  | bash | git     | diff       | git-diff    | matched          | `artifact_persisted`      | 10k-100k |   164 |       164 |    961,903 |   150,052 |
| 72-hour   | detailed  | bash | —       | —          | default     | default_fallback | `intentional_passthrough` | 1k-10k   |   198 |         0 |    147,203 |   147,203 |
| 72-hour   | detailed  | bash | git     | —          | default     | default_fallback | `anti_worsening_rejected` | 1k-10k   |   132 |         0 |    115,599 |   115,599 |
| 72-hour   | detailed  | grep | —       | —          | search      | matched          | `anti_worsening_rejected` | 1-1k     | 1,176 |         0 |    108,299 |   108,299 |
| 72-hour   | detailed  | read | —       | —          | default     | default_fallback | `anti_worsening_rejected` | 1-1k     |   743 |         0 |     96,393 |    96,393 |
| 72-hour   | detailed  | bash | git     | diff       | git-diff    | matched          | `artifact_persisted`      | 1k-10k   |   121 |       121 |    186,394 |    92,349 |
| 72-hour   | detailed  | bash | git     | diff       | git-diff    | matched          | `anti_worsening_rejected` | 1k-10k   |   138 |         0 |     68,745 |    68,745 |
| 72-hour   | detailed  | bash | git     | status     | git-status  | matched          | `anti_worsening_rejected` | 1-1k     |   852 |         0 |     65,071 |    65,071 |
| 72-hour   | detailed  | bash | —       | —          | default     | default_fallback | `intentional_passthrough` | 1-1k     |   501 |         0 |     53,432 |    53,432 |
| 72-hour   | detailed  | bash | git     | —          | default     | default_fallback | `artifact_persisted`      | 1k-10k   |    27 |        27 |     52,781 |    37,538 |
| 72-hour   | detailed  | bash | git     | —          | default     | default_fallback | `anti_worsening_rejected` | 1-1k     |   367 |         0 |     29,916 |    29,916 |
| 72-hour   | detailed  | bash | git     | log        | git-log     | matched          | `anti_worsening_rejected` | 1-1k     |   268 |         0 |     27,971 |    27,971 |
| 72-hour   | detailed  | bash | git     | diff       | default     | default_fallback | `anti_worsening_rejected` | 1k-10k   |    32 |         0 |     24,279 |    24,279 |
| 72-hour   | detailed  | bash | git     | diff       | git-diff    | matched          | `anti_worsening_rejected` | 1-1k     |   404 |         0 |     23,958 |    23,958 |
| 72-hour   | detailed  | bash | git     | diff       | default     | default_fallback | `intentional_passthrough` | 10k-100k |     2 |         0 |     23,712 |    23,712 |
| 72-hour   | detailed  | bash | git     | —          | default     | default_fallback | `anti_worsening_rejected` | 10k-100k |     5 |         0 |     23,219 |    23,219 |
| 72-hour   | detailed  | bash | git     | status     | default     | default_fallback | `anti_worsening_rejected` | 1k-10k   |    27 |         0 |     21,291 |    21,291 |
| 72-hour   | detailed  | bash | git     | diff       | git-diff    | matched          | `intentional_passthrough` | 10k-100k |     5 |         0 |     20,822 |    20,822 |
| 72-hour   | detailed  | bash | git     | status     | default     | default_fallback | `artifact_persisted`      | 10k-100k |    10 |        10 |     56,407 |    18,013 |
| 72-hour   | detailed  | bash | ls      | —          | ls          | matched          | `intentional_passthrough` | 10k-100k |     2 |         0 |     16,943 |    16,943 |
| 72-hour   | detailed  | bash | git     | diff       | default     | default_fallback | `artifact_persisted`      | 10k-100k |     8 |         8 |     37,189 |    16,850 |
| 72-hour   | detailed  | bash | ls      | —          | ls          | matched          | `anti_worsening_rejected` | 1k-10k   |    13 |         0 |     13,792 |    13,792 |
| 72-hour   | detailed  | bash | —       | —          | passthrough | matched          | `intentional_passthrough` | 1k-10k   |    12 |         0 |     10,640 |    10,640 |
| 72-hour   | detailed  | bash | git     | log        | default     | default_fallback | `anti_worsening_rejected` | 1k-10k   |    11 |         0 |      9,823 |     9,823 |
| 72-hour   | detailed  | bash | git     | log        | git-log     | matched          | `artifact_persisted`      | 1k-10k   |    24 |        24 |     15,835 |     7,652 |
| 72-hour   | detailed  | bash | git     | diff       | default     | default_fallback | `anti_worsening_rejected` | 1-1k     |    60 |         0 |      6,703 |     6,703 |
| 72-hour   | detailed  | bash | rg      | —          | search      | matched          | `anti_worsening_rejected` | 1k-10k   |     8 |         0 |      6,555 |     6,555 |
| 72-hour   | detailed  | bash | git     | status     | default     | default_fallback | `anti_worsening_rejected` | 1-1k     |    57 |         0 |      5,568 |     5,568 |
| 72-hour   | detailed  | bash | git     | log        | git-log     | matched          | `anti_worsening_rejected` | 1k-10k   |    16 |         0 |      4,834 |     4,834 |
| 72-hour   | detailed  | bash | git     | diff       | git-diff    | matched          | `intentional_passthrough` | 1k-10k   |     4 |         0 |      4,510 |     4,510 |
| 72-hour   | detailed  | grep | —       | —          | search      | matched          | `artifact_persisted`      | 100k+    |     2 |         2 |     58,338 |     3,689 |
| 72-hour   | detailed  | bash | git     | log        | default     | default_fallback | `anti_worsening_rejected` | 10k-100k |     1 |         0 |      2,610 |     2,610 |
| 72-hour   | detailed  | bash | git     | status     | git-status  | matched          | `intentional_passthrough` | 1k-10k   |     3 |         0 |      2,489 |     2,489 |
| 72-hour   | detailed  | bash | —       | —          | default     | default_fallback | `artifact_persisted`      | 1-1k     |    17 |        17 |      3,091 |     2,298 |
| 72-hour   | detailed  | bash | ls      | —          | ls          | matched          | `anti_worsening_rejected` | 1-1k     |    76 |         0 |      1,940 |     1,940 |
| 72-hour   | detailed  | bash | git     | status     | default     | default_fallback | `artifact_persisted`      | 1k-10k   |     1 |         1 |      2,478 |     1,792 |
| 72-hour   | detailed  | bash | git     | —          | default     | default_fallback | `intentional_passthrough` | 1k-10k   |     3 |         0 |      1,590 |     1,590 |
| 72-hour   | omitted   | bash | git     | —          | default     | default_fallback | `intentional_passthrough` | 1-1k     |    15 |         0 |      1,430 |     1,430 |
| 72-hour   | omitted   | bash | git     | diff       | default     | default_fallback | `intentional_passthrough` | 1k-10k   |     3 |         0 |      1,335 |     1,335 |
| 72-hour   | omitted   | bash | git     | log        | git-log     | matched          | `artifact_persisted`      | 10k-100k |     4 |         4 |     37,121 |     1,178 |
| 72-hour   | omitted   | bash | rg      | —          | search      | matched          | `artifact_persisted`      | 10k-100k |     1 |         1 |      3,504 |       989 |
| 72-hour   | omitted   | bash | git     | log        | default     | default_fallback | `intentional_passthrough` | 1k-10k   |     1 |         0 |        857 |       857 |
| 72-hour   | omitted   | bash | ls      | —          | ls          | matched          | `intentional_passthrough` | 1k-10k   |     2 |         0 |        830 |       830 |
| 72-hour   | omitted   | bash | —       | —          | passthrough | matched          | `intentional_passthrough` | 1-1k     |     8 |         0 |        828 |       828 |
| 72-hour   | omitted   | bash | git     | log        | default     | default_fallback | `anti_worsening_rejected` | 1-1k     |     9 |         0 |        811 |       811 |
| 72-hour   | omitted   | bash | rg      | —          | search      | matched          | `anti_worsening_rejected` | 1-1k     |     8 |         0 |        811 |       811 |
| 72-hour   | omitted   | bash | ls      | —          | ls          | matched          | `intentional_passthrough` | 1-1k     |    10 |         0 |        701 |       701 |
| 72-hour   | omitted   | bash | git     | status     | git-status  | matched          | `intentional_passthrough` | 1-1k     |    10 |         0 |        505 |       505 |
| 72-hour   | omitted   | bash | grep    | —          | search      | matched          | `anti_worsening_rejected` | 1-1k     |     2 |         0 |        371 |       371 |
| 72-hour   | omitted   | bash | git     | status     | default     | default_fallback | `intentional_passthrough` | 1k-10k   |     1 |         0 |        311 |       311 |
| 72-hour   | omitted   | bash | git     | diff       | git-diff    | matched          | `intentional_passthrough` | 1-1k     |     5 |         0 |        251 |       251 |
| 72-hour   | omitted   | bash | git     | status     | default     | default_fallback | `intentional_passthrough` | 1-1k     |     5 |         0 |        210 |       210 |
| 72-hour   | omitted   | bash | git     | log        | git-log     | matched          | `intentional_passthrough` | 1-1k     |     5 |         0 |        116 |       116 |
| 72-hour   | omitted   | bash | git     | log        | git-log     | matched          | `artifact_persisted`      | 1-1k     |     1 |         1 |        211 |       108 |
| 72-hour   | omitted   | bash | rg      | —          | search      | matched          | `intentional_passthrough` | 1-1k     |     1 |         0 |         48 |        48 |
| 72-hour   | omitted   | bash | —       | —          | ls          | matched          | `anti_worsening_rejected` | 1-1k     |     2 |         0 |          4 |         4 |
| seven-day | detailed  | read | —       | —          | default     | default_fallback | `anti_worsening_rejected` | 1k-10k   | 6,030 |         0 |  5,379,955 | 5,379,955 |
| seven-day | detailed  | read | —       | —          | default     | default_fallback | `artifact_persisted`      | 10k-100k | 2,052 |     2,052 | 10,384,440 | 4,054,545 |
| seven-day | detailed  | bash | —       | —          | default     | default_fallback | `artifact_persisted`      | 10k-100k |   912 |       912 |  6,349,173 | 3,157,961 |
| seven-day | detailed  | read | —       | —          | default     | default_fallback | `artifact_persisted`      | 1k-10k   | 1,402 |     1,402 |  2,765,942 | 2,138,036 |
| seven-day | detailed  | bash | —       | —          | default     | default_fallback | `anti_worsening_rejected` | 1k-10k   | 2,620 |         0 |  1,909,535 | 1,909,535 |
| seven-day | detailed  | grep | —       | —          | search      | matched          | `artifact_persisted`      | 10k-100k |   815 |       815 |  3,958,330 | 1,119,000 |
| seven-day | detailed  | grep | —       | —          | search      | matched          | `artifact_persisted`      | 1k-10k   | 1,066 |     1,066 |  1,793,393 |   914,894 |
| seven-day | detailed  | bash | —       | —          | default     | default_fallback | `artifact_persisted`      | 1k-10k   |   884 |       884 |  1,296,882 |   880,495 |
| seven-day | detailed  | grep | —       | —          | search      | matched          | `anti_worsening_rejected` | 1k-10k   | 1,454 |         0 |    875,829 |   875,829 |
| seven-day | detailed  | read | —       | —          | default     | default_fallback | `anti_worsening_rejected` | 10k-100k |   172 |         0 |    762,790 |   762,790 |
| seven-day | detailed  | bash | git     | status     | git-status  | matched          | `anti_worsening_rejected` | 10k-100k |   108 |         0 |    654,843 |   654,843 |
| seven-day | detailed  | bash | —       | —          | default     | default_fallback | `anti_worsening_rejected` | 10k-100k |   115 |         0 |    558,208 |   558,208 |
| seven-day | detailed  | bash | —       | —          | passthrough | matched          | `intentional_passthrough` | 10k-100k |    60 |         0 |    540,061 |   540,061 |
| seven-day | detailed  | bash | ls      | —          | ls          | matched          | `anti_worsening_rejected` | 10k-100k |    71 |         0 |    416,294 |   416,294 |
| seven-day | detailed  | bash | git     | —          | default     | default_fallback | `artifact_persisted`      | 10k-100k |   153 |       153 |  1,087,103 |   349,547 |
| seven-day | detailed  | bash | —       | —          | default     | default_fallback | `intentional_passthrough` | 10k-100k |    57 |         0 |    332,259 |   332,259 |
| seven-day | detailed  | bash | git     | diff       | git-diff    | matched          | `artifact_persisted`      | 10k-100k |   353 |       353 |  2,453,107 |   329,585 |
| seven-day | detailed  | bash | git     | status     | git-status  | matched          | `anti_worsening_rejected` | 1k-10k   |   389 |         0 |    271,922 |   271,922 |
| seven-day | detailed  | bash | —       | —          | default     | default_fallback | `anti_worsening_rejected` | 1-1k     | 3,036 |         0 |    240,616 |   240,616 |
| seven-day | detailed  | bash | —       | —          | default     | default_fallback | `intentional_passthrough` | 1k-10k   |   307 |         0 |    220,047 |   220,047 |
| seven-day | detailed  | bash | git     | —          | default     | default_fallback | `anti_worsening_rejected` | 1k-10k   |   243 |         0 |    216,900 |   216,900 |
| seven-day | detailed  | grep | —       | —          | search      | matched          | `anti_worsening_rejected` | 1-1k     | 2,086 |         0 |    190,323 |   190,323 |
| seven-day | detailed  | bash | git     | diff       | git-diff    | matched          | `artifact_persisted`      | 1k-10k   |   217 |       217 |    335,868 |   173,840 |
| seven-day | detailed  | read | —       | —          | default     | default_fallback | `anti_worsening_rejected` | 1-1k     | 1,087 |         0 |    137,002 |   137,002 |
| seven-day | detailed  | bash | git     | diff       | git-diff    | matched          | `anti_worsening_rejected` | 1k-10k   |   227 |         0 |    116,931 |   116,931 |
| seven-day | detailed  | bash | git     | status     | git-status  | matched          | `anti_worsening_rejected` | 1-1k     | 1,059 |         0 |     84,855 |    84,855 |
| seven-day | detailed  | bash | —       | —          | default     | default_fallback | `intentional_passthrough` | 1-1k     |   755 |         0 |     79,592 |    79,592 |
| seven-day | detailed  | bash | git     | —          | default     | default_fallback | `artifact_persisted`      | 1k-10k   |    33 |        33 |     65,570 |    47,938 |
| seven-day | detailed  | bash | git     | —          | default     | default_fallback | `anti_worsening_rejected` | 10k-100k |    10 |         0 |     42,047 |    42,047 |
| seven-day | detailed  | bash | git     | diff       | default     | default_fallback | `artifact_persisted`      | 10k-100k |    16 |        16 |     89,745 |    41,636 |
| seven-day | detailed  | bash | git     | —          | default     | default_fallback | `anti_worsening_rejected` | 1-1k     |   515 |         0 |     40,590 |    40,590 |
| seven-day | detailed  | bash | git     | diff       | default     | default_fallback | `anti_worsening_rejected` | 1k-10k   |    52 |         0 |     40,301 |    40,301 |
| seven-day | detailed  | bash | git     | diff       | git-diff    | matched          | `intentional_passthrough` | 10k-100k |     7 |         0 |     37,524 |    37,524 |
| seven-day | detailed  | bash | git     | diff       | git-diff    | matched          | `anti_worsening_rejected` | 1-1k     |   607 |         0 |     35,300 |    35,300 |
| seven-day | detailed  | bash | git     | log        | git-log     | matched          | `anti_worsening_rejected` | 1-1k     |   333 |         0 |     33,820 |    33,820 |
| seven-day | detailed  | bash | ls      | —          | ls          | matched          | `intentional_passthrough` | 10k-100k |     3 |         0 |     28,577 |    28,577 |
| seven-day | detailed  | bash | ls      | —          | ls          | matched          | `anti_worsening_rejected` | 1k-10k   |    26 |         0 |     28,312 |    28,312 |
| seven-day | detailed  | bash | git     | status     | default     | default_fallback | `anti_worsening_rejected` | 1k-10k   |    38 |         0 |     27,032 |    27,032 |
| seven-day | detailed  | bash | git     | status     | default     | default_fallback | `artifact_persisted`      | 10k-100k |    12 |        12 |     69,419 |    24,382 |
| seven-day | detailed  | bash | git     | diff       | default     | default_fallback | `intentional_passthrough` | 10k-100k |     2 |         0 |     23,712 |    23,712 |
| seven-day | detailed  | bash | git     | log        | git-log     | matched          | `artifact_persisted`      | 1k-10k   |    68 |        68 |     51,328 |    23,570 |
| seven-day | detailed  | bash | git     | log        | default     | default_fallback | `anti_worsening_rejected` | 1k-10k   |    20 |         0 |     15,614 |    15,614 |
| seven-day | detailed  | bash | —       | —          | passthrough | matched          | `intentional_passthrough` | 1k-10k   |    16 |         0 |     15,171 |    15,171 |
| seven-day | detailed  | bash | rg      | —          | search      | matched          | `intentional_passthrough` | 10k-100k |     1 |         0 |     12,810 |    12,810 |
| seven-day | detailed  | bash | rg      | —          | search      | matched          | `anti_worsening_rejected` | 1k-10k   |    17 |         0 |     12,060 |    12,060 |
| seven-day | detailed  | bash | git     | log        | git-log     | matched          | `anti_worsening_rejected` | 1k-10k   |    28 |         0 |      9,858 |     9,858 |
| seven-day | detailed  | bash | git     | diff       | default     | default_fallback | `anti_worsening_rejected` | 1-1k     |    87 |         0 |      9,383 |     9,383 |
| seven-day | detailed  | bash | rg      | —          | search      | matched          | `artifact_persisted`      | 10k-100k |     6 |         6 |     38,166 |     9,116 |
| seven-day | detailed  | bash | git     | status     | default     | default_fallback | `anti_worsening_rejected` | 1-1k     |    85 |         0 |      8,451 |     8,451 |
| seven-day | detailed  | bash | git     | diff       | git-diff    | matched          | `intentional_passthrough` | 1k-10k   |     7 |         0 |      8,193 |     8,193 |
| seven-day | detailed  | bash | git     | diff       | default     | default_fallback | `intentional_passthrough` | 1k-10k   |    12 |         0 |      7,754 |     7,754 |
| seven-day | detailed  | bash | ls      | —          | ls          | matched          | `intentional_passthrough` | 1k-10k   |     7 |         0 |      6,777 |     6,777 |
| seven-day | detailed  | bash | git     | diff       | default     | default_fallback | `anti_worsening_rejected` | 10k-100k |     1 |         0 |      6,202 |     6,202 |
| seven-day | detailed  | bash | git     | status     | git-status  | matched          | `intentional_passthrough` | 1k-10k   |     7 |         0 |      6,167 |     6,167 |
| seven-day | detailed  | bash | git     | —          | default     | default_fallback | `intentional_passthrough` | 1k-10k   |    11 |         0 |      5,522 |     5,522 |
| seven-day | omitted   | grep | —       | —          | search      | matched          | `artifact_persisted`      | 100k+    |     2 |         2 |     58,338 |     3,689 |
| seven-day | omitted   | bash | —       | —          | default     | default_fallback | `artifact_persisted`      | 1-1k     |    28 |        28 |      5,100 |     3,629 |
| seven-day | omitted   | bash | ls      | —          | ls          | matched          | `anti_worsening_rejected` | 1-1k     |   104 |         0 |      3,444 |     3,444 |
| seven-day | omitted   | bash | grep    | —          | search      | matched          | `anti_worsening_rejected` | 1k-10k   |     4 |         0 |      2,802 |     2,802 |
| seven-day | omitted   | bash | git     | —          | default     | default_fallback | `intentional_passthrough` | 1-1k     |    27 |         0 |      2,611 |     2,611 |
| seven-day | omitted   | bash | git     | log        | default     | default_fallback | `anti_worsening_rejected` | 10k-100k |     1 |         0 |      2,610 |     2,610 |
| seven-day | omitted   | bash | git     | log        | git-log     | matched          | `artifact_persisted`      | 10k-100k |     7 |         7 |     62,292 |     2,256 |
| seven-day | omitted   | bash | git     | status     | default     | default_fallback | `intentional_passthrough` | 1k-10k   |     4 |         0 |      2,217 |     2,217 |
| seven-day | omitted   | bash | git     | diff       | default     | default_fallback | `artifact_persisted`      | 1k-10k   |     1 |         1 |      2,359 |     1,858 |
| seven-day | omitted   | bash | git     | log        | default     | default_fallback | `artifact_persisted`      | 10k-100k |     1 |         1 |     12,824 |     1,856 |
| seven-day | omitted   | bash | git     | log        | default     | default_fallback | `anti_worsening_rejected` | 1-1k     |    22 |         0 |      1,817 |     1,817 |
| seven-day | omitted   | bash | git     | status     | default     | default_fallback | `artifact_persisted`      | 1k-10k   |     1 |         1 |      2,478 |     1,792 |
| seven-day | omitted   | bash | rg      | —          | search      | matched          | `artifact_persisted`      | 1k-10k   |     1 |         1 |      2,227 |     1,215 |
| seven-day | omitted   | bash | grep    | —          | search      | matched          | `anti_worsening_rejected` | 1-1k     |    10 |         0 |      1,127 |     1,127 |
| seven-day | omitted   | bash | rg      | —          | search      | matched          | `anti_worsening_rejected` | 1-1k     |    13 |         0 |        961 |       961 |
| seven-day | omitted   | bash | —       | —          | passthrough | matched          | `intentional_passthrough` | 1-1k     |     9 |         0 |        905 |       905 |
| seven-day | omitted   | bash | git     | log        | default     | default_fallback | `intentional_passthrough` | 1k-10k   |     1 |         0 |        857 |       857 |
| seven-day | omitted   | bash | ls      | —          | ls          | matched          | `intentional_passthrough` | 1-1k     |    13 |         0 |        852 |       852 |
| seven-day | omitted   | bash | git     | status     | git-status  | matched          | `intentional_passthrough` | 1-1k     |    14 |         0 |        796 |       796 |
| seven-day | omitted   | bash | git     | diff       | git-diff    | matched          | `intentional_passthrough` | 1-1k     |     9 |         0 |        697 |       697 |
| seven-day | omitted   | bash | —       | —          | ls          | matched          | `anti_worsening_rejected` | 1-1k     |     4 |         0 |        312 |       312 |
| seven-day | omitted   | bash | git     | diff       | default     | default_fallback | `intentional_passthrough` | 1-1k     |     2 |         0 |        312 |       312 |
| seven-day | omitted   | bash | git     | status     | default     | default_fallback | `intentional_passthrough` | 1-1k     |     6 |         0 |        229 |       229 |
| seven-day | omitted   | bash | —       | —          | ls          | matched          | `intentional_passthrough` | 1-1k     |     2 |         0 |        209 |       209 |
| seven-day | omitted   | bash | git     | —          | default     | default_fallback | `artifact_persisted`      | 1-1k     |     1 |         1 |        183 |       145 |
| seven-day | omitted   | bash | git     | log        | git-log     | matched          | `intentional_passthrough` | 1-1k     |     6 |         0 |        138 |       138 |
| seven-day | omitted   | bash | git     | log        | git-log     | matched          | `artifact_persisted`      | 1-1k     |     1 |         1 |        211 |       108 |
| seven-day | omitted   | bash | rg      | —          | search      | matched          | `intentional_passthrough` | 1-1k     |     3 |         0 |         54 |        54 |
| seven-day | omitted   | bash | —       | —          | find        | matched          | `intentional_passthrough` | 1-1k     |     1 |         0 |          3 |         3 |
