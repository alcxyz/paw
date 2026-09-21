"""Small guardrails complement actionlint's Forgejo-compatible syntax check."""

import pathlib
import re
import unittest


ROOT = pathlib.Path(__file__).resolve().parents[1]


class WorkflowPolicyTests(unittest.TestCase):
    def test_actions_are_pinned_and_checkout_does_not_persist_credentials(self):
        for name in ("validate.yml", "update-dependencies.yml"):
            text = (ROOT / ".forgejo/workflows" / name).read_text()
            actions = re.findall(r"uses: (\S+)", text)
            self.assertTrue(actions)
            for action in actions:
                self.assertRegex(action, r"^https://[^\s@]+@[0-9a-f]{40}$")
            self.assertIn("persist-credentials: false", text)
            self.assertNotIn("pull_request_target", text)
            self.assertNotIn("secrets.", text)
            self.assertNotIn("continue-on-error", text)
            self.assertIn("sandbox = true", text)

    def test_schedule_is_proposal_only(self):
        text = (ROOT / ".forgejo/workflows/update-dependencies.yml").read_text()
        self.assertIn("cron: '17 3 * * *'", text)
        self.assertIn("if: forgejo.ref == 'refs/heads/dev'", text)
        self.assertIn("cancel-in-progress: false", text)
        self.assertIn("scripts/ci/publish-update.py --check-open", text)
        self.assertIn("if: steps.pending.outputs.open != 'true'", text)
        self.assertLess(text.index("run: bash scripts/ci/check.sh"),
                        text.index("- name: Open dependency review"))
        for forbidden in ("kubectl", "docker push", "nix copy", "--force", "merge-pr"):
            self.assertNotIn(forbidden, text)

    def test_build_step_has_no_publication_token(self):
        for name in ("validate.yml", "update-dependencies.yml"):
            text = (ROOT / ".forgejo/workflows" / name).read_text()
            steps = text.split("      - ")
            for step in steps:
                if "run: bash scripts/ci/check.sh" in step or "scripts/update-dependencies.py" in step:
                    self.assertIn('FORGEJO_TOKEN: ""', step)
                    self.assertIn('GITHUB_TOKEN: ""', step)


if __name__ == "__main__":
    unittest.main()
