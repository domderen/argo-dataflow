package argo.dataflow.sdk;

public class Main {
    public static void main(String[] args) throws Exception {
        IHandler handler = new Handler();
        ArgoDataflowSdk.start(handler);
    }
}
