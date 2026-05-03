import { describe, expect, it, vi } from "vitest";

import { createEPMResolver } from "./epm-resolver";

describe("EPMResolver", () => {
  it("does not use a public HTTP IPFS gateway unless one is explicitly configured", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue({
      ok: false,
      status: 404,
    } as Response);
    const resolver = createEPMResolver();

    await expect(resolver.resolveByCID("bafkrei-epm")).resolves.toBeNull();

    expect(fetchMock).not.toHaveBeenCalled();
  });
});
