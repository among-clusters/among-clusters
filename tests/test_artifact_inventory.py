import importlib.util
from pathlib import Path
import unittest


ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location(
    "validate_artifacts", ROOT / "scripts" / "validate_artifacts.py"
)
assert SPEC and SPEC.loader
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class ArtifactInventoryTest(unittest.TestCase):
    def test_inventory_matches_release_sources(self):
        result = MODULE.validate()
        self.assertEqual(result["status"], "valid")
        self.assertIn("runtime-image", result["artifacts"])
        self.assertIn("umbrella-chart", result["artifacts"])

    def test_release_tag_must_match_chart_versions(self):
        with self.assertRaisesRegex(ValueError, "does not match"):
            MODULE.validate("v0.0.0")


if __name__ == "__main__":
    unittest.main()
