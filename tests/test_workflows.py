"""Small safety guardrails complement actionlint's syntax check."""

import pathlib
import re
import unittest


ROOT = pathlib.Path(__file__).resolve().parents[1]


class WorkflowPolicyTests(unittest.TestCase):
    def test_actions_are_pinned_and_checkout_does_not_persist_credentials(self):
        for name in ("validate.yml", "update-dependencies.yml"):
            text = (ROOT / ".github/workflows" / name).read_text()
            actions = re.findall(r"uses: (\S+)", text)
            self.assertTrue(actions)
            for action in actions:
                self.assertRegex(action, r"^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+@[0-9a-f]{40}$")
            self.assertIn("persist-credentials: false", text)
            self.assertNotIn("pull_request_target", text)
            self.assertNotIn("secrets.", text)
            self.assertNotIn("continue-on-error", text)
            self.assertIn("sandbox = true", text)
            self.assertIn("sandbox-fallback = false", text)
            self.assertIn('github_access_token: ""', text)
            self.assertIn("runs-on: ubuntu-24.04", text)

    def test_schedule_is_proposal_only(self):
        text = (ROOT / ".github/workflows/update-dependencies.yml").read_text()
        self.assertIn("cron: '17 3 * * *'", text)
        self.assertIn("if: github.ref == 'refs/heads/dev'", text)
        self.assertIn("cancel-in-progress: false", text)
        self.assertIn("scripts/ci/publish-update.py --check-open", text)
        self.assertIn("--inputs-from . nixpkgs#python3", text)
        self.assertIn("--command python3 scripts/update-dependencies.py", text)
        self.assertIn("if: steps.pending.outputs.open != 'true'", text)
        self.assertLess(text.index("run: bash scripts/ci/check.sh"),
                        text.index("- name: Open dependency review"))
        self.assertLess(text.index("run: bash scripts/ci/check-sandbox.sh"),
                        text.index("- name: Discover stable releases"))
        for forbidden in ("kubectl", "docker push", "nix copy", "--force", "merge-pr"):
            self.assertNotIn(forbidden, text)

    def test_build_step_has_no_publication_token(self):
        for name in ("validate.yml", "update-dependencies.yml"):
            text = (ROOT / ".github/workflows" / name).read_text()
            steps = text.split("      - ")
            for step in steps:
                if ("run: bash scripts/ci/check.sh" in step
                        or "run: bash scripts/ci/check-sandbox.sh" in step
                        or "scripts/update-dependencies.py" in step):
                    self.assertIn('GITHUB_TOKEN: ""', step)


if __name__ == "__main__":
    unittest.main()
