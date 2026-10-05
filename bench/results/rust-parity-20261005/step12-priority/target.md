| Workload | Go req/s | Rust req/s | Go / Rust | Result |
|---|---:|---:|---:|---|
| room_show | 7,408 | 7,246 | 102.2% | PASS |
| messages_page | 8,827 | 8,240 | 107.1% | PASS |
| sidebar | 10,128 | 11,914 | 85.0% | FAIL |
| search | 7,566 | 10,752 | 70.4% | FAIL |
| post_message | 1,930 | 2,840 | 68.0% | FAIL |
