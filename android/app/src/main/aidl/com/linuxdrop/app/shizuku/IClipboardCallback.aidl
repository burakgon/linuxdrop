package com.linuxdrop.app.shizuku;

import android.os.ParcelFileDescriptor;

// Callback from the shell-uid UserService back into the app process.
oneway interface IClipboardCallback {
    void onClipboardText(String text);
    // Image bytes are streamed over a pipe (read end) to dodge the 1 MB binder limit.
    void onClipboardImage(in ParcelFileDescriptor pfd, String mime, long size);
}
