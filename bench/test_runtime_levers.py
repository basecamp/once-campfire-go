#!/usr/bin/env python3
"""Tests for the runtime-lever tooling: bench/gc-policy's GC attribution and parsing, and
bin/pgo-profile's build-info parsing, PGO-input candidate and source identity.

    python3 bench/test_runtime_levers.py

The attribution tests are the synthetic boundary cases for start-attributed, whole-cycle
accounting over half-open windows: a cycle crossing the window start, a cycle crossing the
window end, a cycle exactly on either edge, and a cycle in the gap between windows. The rest pin
the `go version -m` record parsing (the module record's leading tab), the PGO-input candidate
(never an identity, and captured before a run can overwrite it), the source-identity status, and
the decoded-body baseline property that bench/gc-policy now enforces across arms and runs.
"""
import hashlib, importlib.machinery, importlib.util, json, tempfile, unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]


def load(name, path):
    loader = importlib.machinery.SourceFileLoader(name, str(path))
    spec = importlib.util.spec_from_loader(loader.name, loader)
    module = importlib.util.module_from_spec(spec)
    loader.exec_module(module)
    return module


gc = load('gc_policy_under_test', ROOT / 'bench/gc-policy')
pgo = load('pgo_profile_under_test', ROOT / 'bin/pgo-profile')


class GctraceAttribution(unittest.TestCase):
    """Start-attributed, whole-cycle, half-open windows."""

    def cycles(self):
        return [dict(n=1, at=0.5, cpu_ms=1.0),   # before the measured window (warmup)
                dict(n=2, at=2.0, cpu_ms=2.0),   # exactly on the start edge
                dict(n=3, at=3.0, cpu_ms=3.0),   # inside
                dict(n=4, at=4.0, cpu_ms=4.0),   # exactly on the end edge
                dict(n=5, at=5.0, cpu_ms=5.0)]   # after the window

    def test_half_open_window_includes_the_start_edge_and_excludes_the_end_edge(self):
        inside = gc.attribute_cycles(self.cycles(), 2.0, 4.0)
        self.assertEqual([cycle['n'] for cycle in inside], [2, 3])

    def test_cycle_crossing_the_start_boundary_belongs_to_the_warmup(self):
        # A cycle that begins at 1.9 (warmup) and would finish at 2.1 (measured): by start it is
        # the warmup's, and it must not appear in the measured window at all.
        cycles = [dict(n=1, at=1.9, cpu_ms=7.0)]
        self.assertEqual([c['n'] for c in gc.attribute_cycles(cycles, 1.0, 2.0)], [1])
        self.assertEqual(gc.attribute_cycles(cycles, 2.0, 4.0), [])

    def test_cycle_crossing_the_end_boundary_is_counted_whole_in_the_window(self):
        # A cycle that begins at 3.9 (measured) and would finish at 4.1 (after): counted in full
        # in the measured window, not split, and not counted after it.
        cycles = [dict(n=1, at=3.9, cpu_ms=7.0)]
        inside = gc.attribute_cycles(cycles, 2.0, 4.0)
        self.assertEqual([c['n'] for c in inside], [1])
        self.assertEqual(sum(c['cpu_ms'] for c in inside), 7.0)
        self.assertEqual(gc.attribute_cycles(cycles, 4.0, 6.0), [])

    def test_cycle_in_the_gap_between_windows_is_counted_nowhere(self):
        cycles = [dict(n=1, at=5.5, cpu_ms=2.0)]
        self.assertEqual(gc.attribute_cycles(cycles, 0.0, 5.0), [])
        self.assertEqual(gc.attribute_cycles(cycles, 6.0, 9.0), [])

    def test_window_cpu_sums_match_the_attributed_cycles(self):
        cycles = [dict(n=1, at=1.9, cpu_ms=7.0), dict(n=2, at=2.5, cpu_ms=1.0),
                  dict(n=3, at=3.9, cpu_ms=2.0), dict(n=4, at=4.5, cpu_ms=9.0)]
        warmup = gc.attribute_cycles(cycles, 1.0, 2.0)
        measured = gc.attribute_cycles(cycles, 2.0, 4.0)
        self.assertEqual(sum(c['cpu_ms'] for c in warmup), 7.0)
        self.assertEqual(sum(c['cpu_ms'] for c in measured), 3.0)
        self.assertEqual(len(measured), 2)


class GctraceParsing(unittest.TestCase):
    LOG = (
        'gc 2026/10/14 15:21:48 INFO gc policy gc_percent=100 mem_limit=9223372036854775807 mem_limit_unit=bytes\n'
        'gc 1 @0.005s 2%: 0.008+0.32+0.006 ms clock, 0.033+0.033/0.29/0.54+0.025 ms cpu, 3->4->3 MB, 4 MB goal, 0 MB stacks, 0 MB globals, 4 P\n'
        'gc 2 @1.500s 5%: 0.011+0.32+0.005 ms clock, 0.045+0.023/0.27/0.55+0.023 ms cpu, 5->6->3 MB, 6 MB goal, 0 MB stacks, 0 MB globals, 4 P\n'
    )

    def test_cycles_carry_start_timestamp_index_share_and_cpu(self):
        cycles = gc.parse_gctrace(self.LOG)
        self.assertEqual([cycle['n'] for cycle in cycles], [1, 2])
        self.assertAlmostEqual(cycles[0]['at'], 0.005)
        self.assertAlmostEqual(cycles[1]['at'], 1.5)
        self.assertEqual(cycles[1]['share'], 5)
        self.assertAlmostEqual(cycles[0]['cpu_ms'], 0.033 + 0.033 + 0.29 + 0.54 + 0.025)

    def test_the_startup_policy_line_is_not_a_cycle(self):
        self.assertEqual(gc.parse_gctrace(self.LOG.splitlines()[0] + '\n'), [])


class BuildInfoParsing(unittest.TestCase):
    """`go version -m` records are tab-separated with a leading tab; the module record was read as
    null before this test existed."""

    RAW = (
        '/path/campfire: go1.27.1\n'
        '\tpath\tgithub.com/basecamp/once-campfire-go/cmd/campfire\n'
        '\tmod\tgithub.com/basecamp/once-campfire-go\tv0.0.0-20261014144541-ce7c42f0d2b2+dirty\t\n'
        '\tdep\tgithub.com/coder/websocket\tv1.8.15\n'
        '\t=>\t./third_party/websocket\t(devel)\t\n'
        '\tbuild\t-buildmode=exe\n'
        '\tbuild\t-pgo=default.pgo\n'
        '\tbuild\t-tags=sqlite_fts5\n'
        '\tbuild\tvcs.revision=ce7c42f0d2b2879fb6e6c64a9e473fef30390c8d\n'
        '\tbuild\tvcs.modified=true\n'
        '\tbuild\tvcs.time=2026-10-14T12:17:42Z\n'
    )

    def test_module_record_is_parsed(self):
        info = pgo.parse_build_info(self.RAW)
        self.assertEqual(info['module'], 'github.com/basecamp/once-campfire-go')
        self.assertEqual(info['module_version'], 'v0.0.0-20261014144541-ce7c42f0d2b2+dirty')

    def test_build_records_are_parsed(self):
        info = pgo.parse_build_info(self.RAW)
        self.assertEqual(info['build_settings']['-pgo'], 'default.pgo')
        self.assertEqual(info['build_settings']['-tags'], 'sqlite_fts5')
        self.assertEqual(info['vcs_revision'], 'ce7c42f0d2b2879fb6e6c64a9e473fef30390c8d')
        self.assertTrue(info['vcs_modified'])
        self.assertEqual(info['vcs_time'], '2026-10-14T12:17:42Z')

    def test_committed_provenance_has_a_module_and_build_settings(self):
        provenance = json.loads((ROOT / 'cmd/campfire/default.pgo.provenance.json').read_text())
        self.assertTrue(provenance['binary']['module'])
        self.assertIn('-buildmode', provenance['binary']['build_settings'])
        self.assertIn('GOOS', provenance['binary']['build_settings'])


class PgoInputCandidate(unittest.TestCase):
    """The binary records the -pgo path, never the profile's content, so the file at that path is
    a candidate, captured before a run can overwrite it."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.package = Path(self.tmp.name)
        self.profile = self.package / 'default.pgo'
        self.profile.write_bytes(b'profile A')
        self.info = {'build_settings': {'-pgo': 'default.pgo'}}

    def test_candidate_is_labelled_path_only_and_not_proven(self):
        candidate = pgo.pgo_input_candidate(self.info, self.package)
        self.assertTrue(candidate['present'])
        self.assertEqual(candidate['sha256'], hashlib.sha256(b'profile A').hexdigest())
        self.assertIn('path only', candidate['identity'])
        self.assertNotIn('proven_to_be_the_input', candidate)

    def test_candidate_is_captured_before_a_run_overwrites_the_file(self):
        before = pgo.pgo_input_candidate(self.info, self.package)
        self.profile.write_bytes(b'profile B')          # what a run with the default output does
        after = pgo.pgo_input_candidate(self.info, self.package)
        self.assertNotEqual(before, after)
        self.assertEqual(before['sha256'], hashlib.sha256(b'profile A').hexdigest())
        self.assertEqual(after['sha256'], hashlib.sha256(b'profile B').hexdigest())

    def test_binary_built_without_pgo_has_no_candidate(self):
        candidate = pgo.pgo_input_candidate({'build_settings': {'-pgo': 'off'}}, self.package)
        self.assertFalse(candidate['present'])
        self.assertIn('no PGO input', candidate['identity'])


class SourceIdentity(unittest.TestCase):
    """A VCS stamp names a revision and a modified flag, not content: the checkout's hashes can
    stand for the binary only for a clean build of the same revision."""

    HASHES = {'cmd/campfire/main.go': 'x'}

    def test_clean_build_of_this_revision_is_a_match(self):
        identity = pgo.source_identity({'vcs_revision': 'abc', 'vcs_modified': False}, 'abc', False, self.HASHES)
        self.assertIn('describes the profiled binary', identity['status'])
        self.assertEqual(identity['reasons'], [])
        self.assertEqual(identity['go_sources_sha256'], self.HASHES)

    def test_another_revision_is_checkout_only(self):
        identity = pgo.source_identity({'vcs_revision': 'abc', 'vcs_modified': False}, 'def', False, self.HASHES)
        self.assertIn('checkout-only', identity['status'])
        self.assertIn('abc', identity['reasons'][0])

    def test_modified_binary_or_dirty_checkout_is_checkout_only(self):
        self.assertIn('checkout-only',
                      pgo.source_identity({'vcs_revision': 'abc', 'vcs_modified': True}, 'abc', False, self.HASHES)['status'])
        self.assertIn('checkout-only',
                      pgo.source_identity({'vcs_revision': 'abc', 'vcs_modified': False}, 'abc', True, self.HASHES)['status'])


class CheckoutDirtyPaths(unittest.TestCase):
    """The tool's own outputs are not build inputs: a collection about to rewrite the profile
    must not look like a modified source tree."""

    STATUS = ' M bin/build\n M cmd/campfire/default.pgo\n?? cmd/campfire/default.pgo.provenance.json\n'
    # `text()` strips the whole output, so the first line loses its leading space.
    STRIPPED = 'M bin/build\n M cmd/campfire/default.pgo\n'

    def test_the_profile_and_its_provenance_are_excluded(self):
        self.assertEqual(pgo.dirty_paths(self.STATUS), ['bin/build'])

    def test_the_stripped_first_line_still_parses(self):
        self.assertEqual(pgo.dirty_paths(self.STRIPPED), ['bin/build'])

    def test_a_real_source_change_still_counts(self):
        self.assertTrue(pgo.dirty_paths(' M internal/web/render.go\n'))


class CommittedBodyBaselines(unittest.TestCase):
    """bench/gc-policy compares each route's normalized decoded body against one baseline across
    every arm and run; the committed runs must show exactly that."""

    DIRECTORIES = ('bench/results/gc-policy-opt-runtime',
                   'bench/results/gc-policy-warmup-check/warmup1',
                   'bench/results/gc-policy-warmup-check/warmup5',
                   'bench/results/pgo-on-off')

    def test_one_decoded_body_hash_per_route_across_arms_and_runs(self):
        for directory in self.DIRECTORIES:
            raw = json.loads((ROOT / directory / 'raw.json').read_text())
            per_route = {}
            for run in raw['runs']:
                for route, digest in run['body_sha256'].items():
                    per_route.setdefault(route, set()).add(digest)
            self.assertEqual(sorted(per_route), ['messages_page', 'room_show', 'search', 'sidebar'], directory)
            for route, digests in per_route.items():
                self.assertEqual(len(digests), 1, f'{directory}: {route} has {len(digests)} decoded bodies')


if __name__ == '__main__':
    unittest.main(verbosity=2)
