#ifndef VKVM_HID_PROTOCOL_H
#define VKVM_HID_PROTOCOL_H

#include <stdint.h>

#define VKVM_HID_PROTOCOL_VERSION 1u
#define VKVM_HID_KEYBOARD_REPORT_SIZE 8u
#define VKVM_HID_MOUSE_REPORT_SIZE 4u

typedef struct {
  uint8_t modifiers;
  uint8_t reserved;
  uint8_t keys[6];
} vkvm_keyboard_report_t;

typedef struct {
  uint8_t buttons;
  int8_t delta_x;
  int8_t delta_y;
  int8_t wheel;
} vkvm_mouse_report_t;

_Static_assert(sizeof(vkvm_keyboard_report_t) == VKVM_HID_KEYBOARD_REPORT_SIZE,
               "keyboard report ABI changed");
_Static_assert(sizeof(vkvm_mouse_report_t) == VKVM_HID_MOUSE_REPORT_SIZE,
               "mouse report ABI changed");

/* Firmware receives authenticated VKVM input frames and emits these reports.
 * Authentication/session replay protection remains in the network gateway;
 * this header only defines the USB/BLE report ABI. */
int vkvm_hid_send_keyboard(const vkvm_keyboard_report_t *report);
int vkvm_hid_send_mouse(const vkvm_mouse_report_t *report);

#endif
