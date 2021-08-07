package argo.dataflow.sdk;

import com.sun.net.httpserver.HttpServer;
import java.io.ByteArrayOutputStream;
import java.io.InputStreamReader;
import java.net.InetSocketAddress;
import java.nio.charset.StandardCharsets;
import java.util.concurrent.Executors;
import java.util.Collections;
import java.util.HashSet;
import java.util.Set;

public class ArgoDataflowSdk {
    static Set<String> threadNames = new HashSet<>();
    public static void start(IHandler handler) throws Exception {
        ProcessHandle processHandle = ProcessHandle.current();
        System.out.println("Starting web server on pid: " + processHandle.pid());
        var server = HttpServer.create(new InetSocketAddress("localhost", 8080), 0);
        Runtime.getRuntime().addShutdownHook(new Thread() {
            @Override
                public void run() {
                    System.out.println("Inside Add Shutdown Hook: " + ArgoDataflowSdk.threadNames.size());
                    while(ArgoDataflowSdk.threadNames.size() != 0) {
                        continue;
                    }
                    server.stop(0);
                    System.out.println("Server stopped, closing app.");
                }   
            });     
        
        server.setExecutor(Executors.newCachedThreadPool());
        server.createContext("/ready", he -> he.sendResponseHeaders(204, 0));
        server.createContext("/messages", he -> {
            ArgoDataflowSdk.threadNames
              .add(Thread.currentThread().getName());
            // read all input bytes
            var isr = new InputStreamReader(he.getRequestBody(), StandardCharsets.UTF_8);
            var b = 0;
            var in = new ByteArrayOutputStream();
            while ((b = isr.read()) != -1) {
                in.write((char) b);
            }
            isr.close();
            try {
                var out = IHandler.Handle(in.toByteArray(), Collections.<String,String>emptyMap());
                if (out != null) {
                    he.sendResponseHeaders(201, 0);
                    try (var os = he.getResponseBody()) {
                        os.write(out);
                    }
                } else{
                    he.sendResponseHeaders(204, 0);
                }
            } catch (Exception e) {
                he.sendResponseHeaders(500, 0);
                try (var os = he.getResponseBody()) {
                    os.write(e.getMessage().getBytes());
                }
            } finally {
                ArgoDataflowSdk.threadNames
              .remove(Thread.currentThread().getName());
            }
        });
        server.start();
    }
}
