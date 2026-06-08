package android.net;

// Local compile-time mirror of the hidden framework interface, so we can hand the
// tethering service a REAL binder callback — a java.lang.reflect.Proxy returns a null
// asBinder() and cannot be marshalled across binder. At runtime the framework's own
// android.net.IIntResultListener is used (the boot classloader shadows this one), so the
// descriptor and transaction codes match. Single method → transaction code 1.
oneway interface IIntResultListener {
    void onResult(int resultCode) = 1;
}
