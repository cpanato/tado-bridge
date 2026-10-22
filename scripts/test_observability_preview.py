import contextlib
import importlib.util
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest import mock


spec = importlib.util.spec_from_file_location("observability_smoke", Path(__file__).with_name("observability-smoke.py"))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class RetainedPreviewTest(unittest.TestCase):
    def test_room_filter_preserves_names_and_matches_only_that_room(self):
        for value in ["All rooms (mean)", 'Home / Room [1].* + "quoted"', "Home / 測\\room"]:
            decoded = json.loads('"' + module.room_value(value) + '"')
            self.assertEqual(decoded, value)

    def test_stop_targets_only_the_retained_project(self):
        with tempfile.TemporaryDirectory(prefix="tado-observability-test-") as directory:
            root = Path(directory)
            state = root / "preview-state.json"
            state.write_text(json.dumps({"purpose": "tado-bridge-synthetic-preview", "project": root.name, "environment": {}}))
            with mock.patch.object(module.subprocess, "run") as run, contextlib.redirect_stdout(io.StringIO()):
                module.stop(state)
            args = run.call_args.args[0]
            self.assertEqual(args[args.index("--project-name") + 1], root.name)
            self.assertEqual(args[-4:], ["down", "--volumes", "--timeout", "15"])
            self.assertFalse(root.exists())

    def test_stop_rejects_unrelated_state_before_running_docker(self):
        with tempfile.TemporaryDirectory(prefix="tado-observability-test-") as directory:
            root = Path(directory)
            state = root / "preview-state.json"
            state.write_text(json.dumps({"purpose": "other", "project": root.name, "environment": {}}))
            with mock.patch.object(module.subprocess, "run") as run:
                with self.assertRaises(ValueError):
                    module.stop(state)
                run.assert_not_called()
            self.assertTrue(root.exists())


if __name__ == "__main__":
    unittest.main()
