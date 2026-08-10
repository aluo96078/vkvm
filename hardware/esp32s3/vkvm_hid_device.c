/* Optional ESP-IDF/TinyUSB adapter for the report ABI.
 *
 * This file is intentionally not built by the Go module: it requires the
 * board's TinyUSB HID device stack. The network gateway must authenticate and
 * sequence-filter frames before calling these functions.
 */
#include "vkvm_hid_protocol.h"

#if defined(VKVM_USE_TINYUSB)
#include "tusb.h"

int vkvm_hid_send_keyboard(const vkvm_keyboard_report_t *report) {
  if (report == 0 || !tud_hid_ready()) return -1;
  return tud_hid_report(1, report, VKVM_HID_KEYBOARD_REPORT_SIZE) ? 0 : -1;
}

int vkvm_hid_send_mouse(const vkvm_mouse_report_t *report) {
  if (report == 0 || !tud_hid_ready()) return -1;
  return tud_hid_report(2, report, VKVM_HID_MOUSE_REPORT_SIZE) ? 0 : -1;
}
#else

int vkvm_hid_send_keyboard(const vkvm_keyboard_report_t *report) {
  (void)report;
  return -1;
}

int vkvm_hid_send_mouse(const vkvm_mouse_report_t *report) {
  (void)report;
  return -1;
}

#endif
