package argo.dataflow.sdk;

import java.util.Map;

public interface IHandler {
    public static byte[] Handle(byte[] msg, Map<String, String> context) throws Exception {
        System.out.println("Inside handler " + new String(msg));
        return ("hi! " + new String(msg)).getBytes("UTF-8");
    }
}
