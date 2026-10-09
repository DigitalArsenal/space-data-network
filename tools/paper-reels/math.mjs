// Every equation the paper reels typeset, as TeX, each as its paper states
// it. typeset.mjs renders them to SVG paths in math.gen.js; edit here, then
// rerun it.

export const TEX = {
  // Evidence-Supported ASO Catalog
  cat_handoff: String.raw`\mathrm{SGP4}(\text{OMM},\ \Delta t = 0) \;\longrightarrow\; (\mathbf r, \mathbf v)_{\mathrm{TEME}} \;\longrightarrow\; (\mathbf r, \mathbf v)_{\mathrm{GCRF}}`,
  cat_dyn: String.raw`\ddot{\mathbf r} = \mathbf a_{\mathrm{gen}}(\mathbf r,\dot{\mathbf r},t) + \mathbf a_{\mathrm{obj}}(\mathbf r,\dot{\mathbf r},t;\mathbf p) + \boldsymbol\delta(t)`,
  cat_orekit: String.raw`\max_{63\ \text{cases},\ 24\ \text{h}} \big\lVert \mathbf r_{\mathrm{HPOP}} - \mathbf r_{\mathrm{Orekit}} \big\rVert \le 15\ \text{mm}`,
  cat_cov: String.raw`P(t) = \Phi(t,t_0)\,P_0\,\Phi(t,t_0)^{\mathsf T} + Q(t)`,
  cat_dn: String.raw`\frac{dn}{n}`,
  cat_rms: String.raw`P \;\leftarrow\; \max(1,\ \mathrm{RMS}_w)^2\,P`,

  // Fast All-vs-All Conjunction Screening
  ca_pairs: String.raw`\binom{n}{2} = \frac{n(n-1)}{2}`,
  ca_bound: String.raw`\lvert \mathbf r(t_k+\tau) - \mathbf r_k - \mathbf v_k\tau \rvert \le D, \qquad \lvert\tau\rvert \le h`,
  ca_sgp4: String.raw`D = \tfrac12 A h^2, \qquad A = \frac{1.05\,\mu}{r_{\min}^2}`,
  ca_cheb: String.raw`D = \tfrac12 A h^2 + (J + G)\,h + P`,
  ca_test: String.raw`\min_{\lvert\tau\rvert \le h} \lvert \Delta\mathbf r + \Delta\mathbf v\,\tau \rvert \le d + D_1 + D_2`,
  ca_newton: String.raw`f = \Delta\mathbf r \cdot \Delta\mathbf v, \qquad \delta t = -\frac{f}{\lvert \Delta\mathbf v \rvert^2}`,
  ca_q: String.raw`q = \frac{\lvert \Delta\mathbf r \rvert_{\max} A}{\lvert \Delta\dot{\mathbf r} \rvert_{\min}^2} < 1`,
  ca_pcmax: String.raw`P_{c,\max} = \max_{\text{covariance size}} P_c\big(d_{\text{miss}},\ R\big)`,
  ca_he: String.raw`\lvert \mathbf a - \mathbf b \rvert^2 = \lvert \mathbf a \rvert^2 - 2\,\mathbf a \cdot \mathbf b + \lvert \mathbf b \rvert^2`,

  // Persistent Adversarial Security
  adv_bond: String.raw`S(k) \ge F(k)`,
  adv_trust: String.raw`T(a) = f\big(W(a),\ V(a),\ D(a)\big)`,
  adv_cred: String.raw`B > P(\text{detection})\,C(\text{fraud}) + P(\neg\,\text{detection})\,G(\text{fraud})`,
  adv_decay: String.raw`\mathrm{trust}(t) = \max\!\left(0,\ 1 - (t/T)^2\right)`,
};
