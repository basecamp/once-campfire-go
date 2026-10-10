
## normal profile, candidate B (blocks 05-normal-main, 06-normal-b, 07-normal-b, 08-normal-main, 21-normal-main, 22-normal-b, 23-normal-b, 24-normal-main)

| Route | Base median (min–max) | Candidate median (min–max) | Change | n |
|---|---:|---:|---:|---:|
| Room page | 22,604 (19,177–25,430) | 25,168 (21,193–26,033) | +11.3% | 8/8 |
| Messages page | 24,456 (19,153–26,573) | 25,379 (13,220–26,695) | +3.8% | 8/8 |
| Sidebar | 26,586 (15,635–28,568) | 27,862 (4,578–28,670) | +4.8% | 8/8 |
| Search | 26,471 (6,199–28,416) | 26,960 (22,109–27,867) | +1.8% | 8/8 |
| Post a message | 3,654 (2,195–3,772) | 3,862 (3,542–3,963) | +5.7% | 8/8 |

| Peak memory per round | Base | Candidate |
|---|---:|---:|
| Server process peak RSS (VmHWM), median (min–max) MiB | 133.9 (131.7–135.7), 8 rounds | 135.1 (132.3–136.4), 8 rounds |
| Container cgroup memory.peak, median (min–max) MiB | 136.9 (132.8–139.7), 8 rounds | 140.5 (133.4–160.1), 8 rounds |

base: validated timed responses 6,304,321, invalid 0, errors 0, peak generator CPU 128.7%

cand: validated timed responses 6,559,730, invalid 0, errors 0, peak generator CPU 132.0%

## mixed profile, candidate B (blocks 13-mixed-main, 14-mixed-b, 15-mixed-b, 16-mixed-main)

| Route | Base median (min–max) | Candidate median (min–max) | Change | n |
|---|---:|---:|---:|---:|
| Room page | 20,432 (16,929–23,838) | 24,326 (23,941–24,635) | +19.1% | 4/4 |
| Messages page | 21,512 (18,878–23,626) | 24,739 (24,092–25,070) | +15.0% | 4/4 |
| Sidebar | 28,770 (23,713–31,495) | 31,920 (30,718–32,157) | +10.9% | 4/4 |
| Search | 28,045 (21,866–29,964) | 30,379 (29,937–30,653) | +8.3% | 4/4 |

| Peak memory per round | Base | Candidate |
|---|---:|---:|
| Server process peak RSS (VmHWM), median (min–max) MiB | 135.4 (133.6–137.2), 4 rounds | 133.4 (129.7–134.9), 4 rounds |
| Container cgroup memory.peak, median (min–max) MiB | 124.7 (122.4–134.6), 4 rounds | 121.7 (118.3–138.1), 4 rounds |

base: validated timed responses 3,103,390, invalid 0, errors 0, peak generator CPU 140.3%, timed mixed writes 1264, writer req/s 9.8–9.9

cand: validated timed responses 3,552,117, invalid 0, errors 0, peak generator CPU 138.8%, timed mixed writes 1264, writer req/s 9.8–9.9
