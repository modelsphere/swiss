import type { Requires } from "@/lib/api";

// Vendor decides the extended resource a pod requests and the node label its
// product is published under, so it is part of what a variant runs on rather
// than decoration. Absent means nvidia.
const VENDORS: Record<string, string> = {
  nvidia: "NVIDIA",
  ascend: "Ascend",
  cambricon: "Cambricon",
  hygon: "Hygon",
  amd: "AMD",
};

export function vendorLabel(vendor?: string): string {
  return VENDORS[vendor ?? "nvidia"] ?? (vendor ?? "");
}

// The cards a variant accepts: the products it names, or any of its vendor's.
export function gpuSupport(r: Requires): string {
  return r.gpuProduct?.length ? r.gpuProduct.join(", ") : `any ${vendorLabel(r.vendor)}`;
}

export function gpuCount(r: Requires): string {
  return r.nodes && r.nodes > 1 ? `${r.gpus} GPU × ${r.nodes} nodes` : `${r.gpus} GPU`;
}

export const VENDOR_RESOURCES: Record<string, string> = {
  nvidia: "nvidia.com/gpu",
  ascend: "huawei.com/Ascend910",
  cambricon: "cambricon.com/mlu",
  hygon: "hygon.com/dcu",
  amd: "amd.com/gpu",
};

export function matchesVendor(
  vendor: string | undefined,
  node: { GPUResource?: string; GPUProduct?: string },
): boolean {
  const v = vendor ?? "nvidia";
  const expected = VENDOR_RESOURCES[v];
  if (node.GPUResource) {
    return node.GPUResource === expected;
  }
  if (expected) {
    return node.GPUProduct === expected;
  }
  return true;
}
