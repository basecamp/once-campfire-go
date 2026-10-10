
## normal profile, candidate A (blocks 01-normal-main, 02-normal-a, 03-normal-a, 04-normal-main, 17-normal-main, 18-normal-a, 19-normal-a, 20-normal-main)

| Route | Base median (min–max) | Candidate median (min–max) | Change | n |
|---|---:|---:|---:|---:|
| Room page | 22,132 (20,168–25,508) | 22,781 (5,492–26,170) | +2.9% | 8/8 |
| Messages page | 23,322 (21,887–26,166) | 25,157 (3,481–27,131) | +7.9% | 8/8 |
| Sidebar | 26,070 (22,312–28,996) | 25,766 (14,142–28,622) | -1.2% | 8/8 |
| Search | 25,428 (6,899–28,309) | 24,759 (8,923–27,929) | -2.6% | 8/8 |
| Post a message | 3,511 (1,103–3,749) | 3,125 (1,935–3,753) | -11.0% | 8/8 |

| Peak memory per round | Base | Candidate |
|---|---:|---:|
| Server process peak RSS (VmHWM), median (min–max) MiB | 134.3 (129.3–135.0), 8 rounds | 133.5 (131.6–138.0), 8 rounds |
| Container cgroup memory.peak, median (min–max) MiB | 135.3 (127.1–137.6), 8 rounds | 136.3 (128.5–145.7), 8 rounds |

base: validated timed responses 6,352,610, invalid 0, errors 0, peak generator CPU 129.4%

cand: validated timed responses 5,816,624, invalid 0, errors 0, peak generator CPU 134.8%

## mixed profile, candidate A (blocks 09-mixed-main, 10-mixed-a, 11-mixed-a, 12-mixed-main)

| Route | Base median (min–max) | Candidate median (min–max) | Change | n |
|---|---:|---:|---:|---:|
| Room page | 17,384 (11,193–22,349) | 18,978 (9,355–23,820) | +9.2% | 4/4 |
| Messages page | 19,121 (4,180–22,073) | 23,939 (3,942–24,492) | +25.2% | 4/4 |
| Sidebar | 28,536 (18,280–29,518) | 30,649 (24,850–31,284) | +7.4% | 4/4 |
| Search | 23,578 (10,271–28,410) | 28,822 (26,756–30,342) | +22.2% | 4/4 |

| Peak memory per round | Base | Candidate |
|---|---:|---:|
| Server process peak RSS (VmHWM), median (min–max) MiB | 136.6 (135.8–151.8), 4 rounds | 134.7 (131.9–149.2), 4 rounds |
| Container cgroup memory.peak, median (min–max) MiB | 134.0 (126.1–144.7), 4 rounds | 132.9 (126.0–141.3), 4 rounds |

base: validated timed responses 2,588,918, invalid 0, errors 0, peak generator CPU 126.5%, timed mixed writes 1260, writer req/s 9.6–9.9

cand: validated timed responses 3,037,817, invalid 0, errors 0, peak generator CPU 128.1%, timed mixed writes 1260, writer req/s 9.6–9.9
