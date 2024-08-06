OBS Remote Control Relay
========================

Systemd service
---------------

/etc/systemd/system/obs-remote-control-relay.service

.. code-block:: ini

   [Unit]
   Description=OBS Remote Control Relay
   After=network.target
   StartLimitIntervalSec=0

   [Service]
   Type=simple
   Restart=always
   RestartSec=1
   User=erik
   ExecStart=/home/erik/obs-remote-control-relay/obs-remote-control-relay -address localhost:9999
   WorkingDirectory=/home/erik/obs-remote-control-relay
   KillSignal=SIGINT

   [Install]
   WantedBy=multi-user.target

Enable it for automatic start at boot.

.. code-block:: text

   sudo systemctl enable obs-remote-control-relay

Start it.

.. code-block:: text

   sudo systemctl start obs-remote-control-relay

Nginx
-----

.. code-block:: text

   location /obs-remote-control-relay/ {
       proxy_pass http://localhost:9999/;
       proxy_http_version  1.1;
       proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
       proxy_set_header Upgrade $http_upgrade;
       proxy_set_header Connection "Upgrade";
       proxy_set_header Host $host;
   }
