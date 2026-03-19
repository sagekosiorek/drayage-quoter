DELETE FROM ports WHERE (name, type) IN (
    ('Wimington', 'Seaport'),
    ('Test Seaport', 'Seaport'),
    ('Test Rail Ramp', 'Rail Ramp')
);
