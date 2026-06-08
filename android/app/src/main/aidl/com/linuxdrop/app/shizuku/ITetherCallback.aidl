package com.linuxdrop.app.shizuku;

oneway interface ITetherCallback {
    // reason: 1 = no keepalive within the safety window.
    void onAutoOff(int reason) = 1;
}
