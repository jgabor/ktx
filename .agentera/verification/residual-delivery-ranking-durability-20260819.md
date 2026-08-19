# Residual-delivery ranking durability record

Source: `historical-aggregates-20260812-19.md`  
Source SHA-256: `2af75efe91d7754dd60fc41f8d6b6c7e264f2eddecbaec1b21eafe7688d55f17`

Raw-delivered ranking uses `anti_worsening_rejected` and `intentional_passthrough`, where raw and delivered estimates are equal; persisted artifacts are excluded.

| Window | Ranking by delivered estimated tokens                                                                 |
| ------ | ----------------------------------------------------------------------------------------------------- |
| 72h    | read/rejected 4,276,104; bash/rejected 3,670,376; bash/passthrough 908,955; grep/rejected 662,313     |
| 7d     | read/rejected 6,279,747; bash/rejected 4,792,147; bash/passthrough 1,334,046; grep/rejected 1,066,152 |

Tool totals change the lead: bash 4,579,331 versus read 4,276,104 in 72h; read 6,279,747 versus bash 6,126,193 in 7d.

Target omitted delivered is 9,419 (72h) and 22,953 (7d); whole-report omitted delivered is 24,531 and 99,255. These amounts cannot overtake known broad groups.

No candidate is selected. The source lacks profile, route, normalized command/subcommand, and output-size-band rows needed to identify a reproducible bounded subgroup; absent target dispositions are not proven zero.

Continuity remains unknown. Cache is separate. Defaults remain unchanged.
