import java.util.Map;

public class Handler {
    public static byte[] Handle(byte[] msg, Map<String, String> context) throws Exception {
        System.out.println("Inside handler " + new String(msg));
        Thread.sleep(10000);
        return ("hi! " + new String(msg)).getBytes("UTF-8");
    }
}