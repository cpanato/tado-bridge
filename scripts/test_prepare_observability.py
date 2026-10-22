import json
import importlib.util
import os
from pathlib import Path
import stat
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("prepare_observability", Path(__file__).with_name("prepare-observability.py"))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
prepare = module.prepare


class PrepareObservabilityTest(unittest.TestCase):
    def test_private_files_and_datasource_mapping_preserve_state(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            prepare(root, "synthetic-token")
            self.assertEqual((root / "api-token").read_text(), "synthetic-token")
            self.assertEqual(stat.S_IMODE((root / "api-token").stat().st_mode), 0o600)
            data = (root / "dashboards/tado-bridge.json").read_text()
            dashboard = json.loads(data)
            self.assertNotIn("__inputs", dashboard)
            self.assertNotIn("${DS_", data)
            self.assertIn("tado-bridge-prometheus", data)
            self.assertIn("tado-bridge-postgres", data)
            sentinel = root / "grafana/existing-state"
            sentinel.write_text("keep")
            prepare(root, "rotated-synthetic-token")
            self.assertEqual(sentinel.read_text(), "keep")

    def test_rejects_symlink_secret_without_touching_target(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            target = root / "unrelated"
            target.write_text("keep")
            (root / "api-token").symlink_to(target)
            with self.assertRaises(OSError):
                prepare(root, "synthetic-token")
            self.assertEqual(target.read_text(), "keep")

    def test_rejects_shared_directory_and_invalid_token(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for token in ("", "secret\n", "secret with spaces"):
                with self.assertRaises(ValueError):
                    prepare(root, token)
            os.chmod(root, 0o755)
            with self.assertRaises(ValueError):
                prepare(root, "synthetic-token")


if __name__ == "__main__":
    unittest.main()
