package android.content;

// Hidden framework interface, bundled for compile-time symbols only. At runtime
// the platform (bootclasspath) class is used. It is a single oneway method, so
// its transaction code is FIRST_CALL_TRANSACTION (1) on every Android version —
// safe to bundle. We register an instance of this with IClipboard so the system
// notifies us of clipboard changes in the background (allowed because the caller
// presents package "com.android.shell", which holds READ_CLIPBOARD_IN_BACKGROUND).
oneway interface IOnPrimaryClipChangedListener {
    void dispatchPrimaryClipChanged();
}
