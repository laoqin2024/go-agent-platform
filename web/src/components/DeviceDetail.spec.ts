import { mount } from "@vue/test-utils";
import { describe, expect, it, vi } from "vitest";
import DeviceDetail from "./DeviceDetail.vue";
import { request } from "../utils/request";

const baseDevice = {
  deviceId: "test-device-001",
  device_id: "test-device-001",
  processes: [],
  softwareList: [],
  hostMetrics: null,
  hardwareDetails: null,
  networkConnections: null,
  securitySnapshot: null,
  serviceSnapshot: null,
  processSnapshot: null,
  softwareInventory: null,
  updatedAtSec: Math.floor(Date.now() / 1000),
  snapshotFound: true,
  wsReceived: true,
};

describe("DeviceDetail USB status", () => {
  it("renders USB enabled text when usb policy/logs APIs return normal data", async () => {
    const getSpy = vi.spyOn(request, "get").mockImplementation(async (url: string) => {
      if (url.includes("/usb_policy")) {
        return {
          data: {
            device_id: "test-device-001",
            disabled: false,
          },
        } as any;
      }
      if (url.includes("/usb_logs")) {
        return {
          data: {
            device_id: "test-device-001",
            items: [
              {
                action: "insert",
                created_at: "2026-04-08T09:25:14Z",
                host_device_id: "test-device-001",
                usb_id: "USBSTOR\\DISK&VEN_TEST&PROD_TEST\\ABC&0",
                volume_name: "TEST_USB",
              },
            ],
          },
        } as any;
      }
      return { data: { items: [] } } as any;
    });

    const wrapper = mount(DeviceDetail, {
      props: {
        device: baseDevice,
        wsStatus: "online",
      },
    });

    // Wait for immediate watch-triggered async API calls to settle.
    await Promise.resolve();
    await Promise.resolve();
    await wrapper.vm.$nextTick();

    const text = wrapper.text();
    expect(text).toContain("USB 已启用");
    expect(getSpy).toHaveBeenCalled();

    getSpy.mockRestore();
  });
});
