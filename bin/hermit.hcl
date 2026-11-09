manage-git = false

// Use GitHub's release API; direct asset downloads can time out in orbs.
// Public releases work without a token.
github-token-auth {
  match = ["gohugoio/hugo"]
}
